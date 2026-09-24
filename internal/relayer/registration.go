package relayer

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"errors"
	"fmt"
	"math/big"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
)

type (
	// RegistrationProof is the part of a relay request that asks clc-core to
	// map a WebAuthn credential to a wallet. Every field is caller-supplied;
	// VerifyRegistration decides whether the caller is entitled to the mapping.
	RegistrationProof struct {
		UserAddress   common.Address
		SignedBatch   apiresp.CaliburSignedBatch
		Signature     []byte // abi.encode(bytes signature, bytes hookData)
		SignatureType string
		PublicKeyX    string // decimal
		PublicKeyY    string // decimal
	}

	// registerKey mirrors Calibur's Key struct for ABI packing. Field names
	// must match the tuple component names below in camel case.
	registerKey struct {
		KeyType   uint8
		PublicKey []byte
	}
)

// ErrInvalidRegistration marks a registration the caller is not entitled to.
// The API maps it to a 400 before anything is queued.
var ErrInvalidRegistration = errors.New("invalid webauthn registration")

var (
	registerKeyArgs = mustRegisterKeyArgs()
	wrappedSigArgs  = mustWrappedSigArgs()

	funcIsRegistered = w3.MustNewFunc("isRegistered(bytes32)", "bool")
)

func mustRegisterKeyArgs() abi.Arguments {
	t, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{Name: "keyType", Type: "uint8"},
		{Name: "publicKey", Type: "bytes"},
	})
	if err != nil {
		panic(err)
	}
	return abi.Arguments{{Type: t}}
}

func mustWrappedSigArgs() abi.Arguments {
	t, err := abi.NewType("bytes", "", nil)
	if err != nil {
		panic(err)
	}
	return abi.Arguments{{Type: t}, {Type: t}}
}

func invalidRegistration(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRegistration, fmt.Sprintf(format, args...))
}

// VerifyRegistration proves that the wallet's root key authorised mapping the
// supplied P-256 key to it, and returns the key hash computed server-side.
//
// POST /relayer/relay is public because first-time registration happens before
// the holder has a session, so the proof has to come from the request itself:
//
//   - the batch is signed by the root key (keyHash 0) with a secp256k1
//     signature that recovers to UserAddress over the exact EIP-712 digest the
//     relayer will submit (the Calibur domain binds that address too);
//   - the batch is exactly one zero-value self-call to register() carrying a
//     WebAuthn P-256 key whose coordinates are the supplied ones, and reverts
//     on failure, so a successful receipt means the key is on the wallet.
//
// Without this an anonymous caller could name a victim's address beside their
// own passkey and later sign in as the victim, whatever happened on chain.
func (r *Relayer) VerifyRegistration(p RegistrationProof) (common.Hash, error) {
	if p.SignatureType != "secp256k1" {
		return common.Hash{}, invalidRegistration("signatureType must be secp256k1, got %q", p.SignatureType)
	}

	x, okX := new(big.Int).SetString(p.PublicKeyX, 10)
	y, okY := new(big.Int).SetString(p.PublicKeyY, 10)
	if !okX || !okY || x.Sign() <= 0 || y.Sign() <= 0 {
		return common.Hash{}, invalidRegistration("publicKeyX / publicKeyY must be positive decimal integers")
	}
	if !elliptic.P256().IsOnCurve(x, y) {
		return common.Hash{}, invalidRegistration("public key is not a P-256 point")
	}

	batch, err := convertSignedBatch(p.SignedBatch)
	if err != nil {
		return common.Hash{}, invalidRegistration("%v", err)
	}
	if batch.KeyHash != ([32]byte{}) {
		return common.Hash{}, invalidRegistration("registration must be signed by the root key")
	}

	// A batch that tolerates a failed call can succeed on chain without the key
	// being registered, and a successful receipt is what activates the row.
	if !batch.BatchedCall.RevertOnFailure {
		return common.Hash{}, invalidRegistration("registration batch must revert on failure")
	}

	calls := batch.BatchedCall.Calls
	if len(calls) != 1 {
		return common.Hash{}, invalidRegistration("registration batch must hold exactly one call, got %d", len(calls))
	}
	call := calls[0]
	if call.To != p.UserAddress {
		return common.Hash{}, invalidRegistration("register call must target the wallet itself")
	}
	if call.Value == nil || call.Value.Sign() != 0 {
		return common.Hash{}, invalidRegistration("register call must carry zero value")
	}
	expected, err := EncodeWebAuthnRegisterCalldata(x, y)
	if err != nil {
		return common.Hash{}, err
	}
	if !bytes.Equal(call.Data, expected) {
		return common.Hash{}, invalidRegistration("register calldata does not match the supplied public key")
	}

	rawSig, err := unwrapRootSignature(p.Signature)
	if err != nil {
		return common.Hash{}, invalidRegistration("%v", err)
	}
	domain := gocalibur.CaliburDomain(r.chainID, p.UserAddress, r.caliburAddress)
	digest := gocalibur.ComputeDigest(domain, batch)
	signer, err := recoverSigner(digest[:], rawSig)
	if err != nil {
		return common.Hash{}, invalidRegistration("%v", err)
	}
	if signer != p.UserAddress {
		return common.Hash{}, invalidRegistration("root signature was not produced by the wallet")
	}

	return ComputeP256KeyHash(x, y), nil
}

// EncodeWebAuthnRegisterCalldata is register((uint8,bytes)) for a WebAuthn
// P-256 key, byte-identical to encodeRegisterCalldata in the dapp's
// @clc/core/features/webauthn/logic.ts.
func EncodeWebAuthnRegisterCalldata(x, y *big.Int) ([]byte, error) {
	key := gocalibur.NewWebAuthnP256Key(x, y)
	packed, err := registerKeyArgs.Pack(registerKey{KeyType: uint8(key.KeyType), PublicKey: key.PublicKey})
	if err != nil {
		return nil, fmt.Errorf("relay: pack register key: %w", err)
	}
	return append(gocalibur.FuncRegister.Selector[:], packed...), nil
}

// unwrapRootSignature decodes Calibur's root-key wrapping,
// abi.encode(bytes signature, bytes hookData), and returns the 65-byte
// secp256k1 signature.
func unwrapRootSignature(wrapped []byte) ([]byte, error) {
	values, err := wrappedSigArgs.Unpack(wrapped)
	if err != nil || len(values) != 2 {
		return nil, errors.New("signature is not abi.encode(bytes, bytes)")
	}
	sig, ok := values[0].([]byte)
	if !ok || len(sig) != crypto.SignatureLength {
		return nil, errors.New("root signature must be 65 bytes")
	}
	// Re-encode and compare so trailing bytes or a non-canonical layout cannot
	// ride along into the transaction the relayer signs.
	reencoded, err := wrappedSigArgs.Pack(sig, values[1].([]byte))
	if err != nil || !bytes.Equal(reencoded, wrapped) {
		return nil, errors.New("signature wrapping is not canonical")
	}
	return sig, nil
}

// recoverSigner recovers the secp256k1 signer of digest, accepting v as 0/1 or
// 27/28 and rejecting high-s signatures as OpenZeppelin's ECDSA.recover does.
func recoverSigner(digest, sig []byte) (common.Address, error) {
	normalized := make([]byte, len(sig))
	copy(normalized, sig)
	if normalized[64] >= 27 {
		normalized[64] -= 27
	}
	r := new(big.Int).SetBytes(normalized[:32])
	s := new(big.Int).SetBytes(normalized[32:64])
	if !crypto.ValidateSignatureValues(normalized[64], r, s, true) {
		return common.Address{}, errors.New("malformed secp256k1 signature")
	}
	pub, err := crypto.SigToPub(digest, normalized)
	if err != nil {
		return common.Address{}, fmt.Errorf("recover signer: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// IsKeyRegistered reads Calibur's isRegistered(keyHash) on the wallet. Used
// only by the one-time legacy credential check, never on a sign-in path.
func (r *Relayer) IsKeyRegistered(ctx context.Context, userAddr common.Address, keyHash common.Hash) (bool, error) {
	var registered bool
	if err := r.chainProvider.Client.CallCtx(ctx,
		eth.CallFunc(userAddr, funcIsRegistered, keyHash).Returns(&registered),
	); err != nil {
		return false, err
	}
	return registered, nil
}
