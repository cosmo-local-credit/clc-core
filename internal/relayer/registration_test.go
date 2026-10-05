package relayer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"math/big"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

type (
	registrationFixture struct {
		rootKey *ecdsa.PrivateKey
		user    common.Address
		x, y    *big.Int
	}
)

func newRegistrationFixture(t *testing.T) registrationFixture {
	t.Helper()
	rootKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	passkey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return registrationFixture{
		rootKey: rootKey,
		user:    crypto.PubkeyToAddress(rootKey.PublicKey),
		x:       passkey.X,
		y:       passkey.Y,
	}
}

func (f registrationFixture) batch(t *testing.T, calls ...apiresp.CaliburCall) apiresp.CaliburSignedBatch {
	t.Helper()
	if calls == nil {
		data, err := EncodeWebAuthnRegisterCalldata(f.x, f.y)
		if err != nil {
			t.Fatal(err)
		}
		calls = []apiresp.CaliburCall{{To: f.user.Hex(), Value: "0", Data: hexutil.Encode(data)}}
	}
	return apiresp.CaliburSignedBatch{
		BatchedCall: apiresp.CaliburBatchedCall{Calls: calls, RevertOnFailure: true},
		Nonce:       "0",
		KeyHash:     common.Hash{}.Hex(),
		Executor:    common.Address{}.Hex(),
		Deadline:    "0",
	}
}

// sign produces the wrapped root signature the dapp sends:
// abi.encode(bytes sig, bytes hookData) over the Calibur digest for domainUser.
func (f registrationFixture) sign(t *testing.T, r *Relayer, key *ecdsa.PrivateKey, domainUser common.Address, batch apiresp.CaliburSignedBatch) []byte {
	t.Helper()
	converted, err := convertSignedBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	digest := gocalibur.ComputeDigest(gocalibur.CaliburDomain(r.chainID, domainUser, r.caliburAddress), converted)
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27 // viem emits v as 27/28
	wrapped, err := wrappedSigArgs.Pack(sig, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func (f registrationFixture) proof(batch apiresp.CaliburSignedBatch, sig []byte) RegistrationProof {
	return RegistrationProof{
		UserAddress:   f.user,
		SignedBatch:   batch,
		Signature:     sig,
		SignatureType: "secp256k1",
		PublicKeyX:    f.x.String(),
		PublicKeyY:    f.y.String(),
	}
}

// TestRegisterCalldataMatchesDapp pins EncodeWebAuthnRegisterCalldata to the
// bytes viem produces for encodeRegisterCalldata(x, y) in
// @clc/core/features/webauthn/logic.ts (P-256 generator point as x, y).
func TestRegisterCalldataMatchesDapp(t *testing.T) {
	const golden = "0x30b1fa3b" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296" +
		"4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5"

	got, err := EncodeWebAuthnRegisterCalldata(elliptic.P256().Params().Gx, elliptic.P256().Params().Gy)
	if err != nil {
		t.Fatal(err)
	}
	if hexutil.Encode(got) != golden {
		t.Fatalf("register calldata = %s, want %s (Go<->TS divergence?)", hexutil.Encode(got), golden)
	}
}

func TestVerifyRegistrationAcceptsRootSignedSelfRegister(t *testing.T) {
	r := testRelayer()
	f := newRegistrationFixture(t)
	batch := f.batch(t)

	keyHash, err := r.VerifyRegistration(f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch)))
	if err != nil {
		t.Fatalf("valid registration rejected: %v", err)
	}
	if keyHash != ComputeP256KeyHash(f.x, f.y) {
		t.Fatalf("keyHash = %s, want server-computed hash", keyHash.Hex())
	}
}

func TestVerifyRegistrationRejects(t *testing.T) {
	r := testRelayer()

	cases := map[string]func(t *testing.T, f registrationFixture) RegistrationProof{
		// The attack: an attacker's own root key registers their passkey on
		// their own wallet, then the request names the victim's address.
		"victim address with attacker credential": func(t *testing.T, f registrationFixture) RegistrationProof {
			attacker := newRegistrationFixture(t)
			victim := f.user
			attacker.user = victim
			batch := attacker.batch(t)
			p := attacker.proof(batch, attacker.sign(t, r, attacker.rootKey, victim, batch))
			return p
		},
		"attacker signs for own wallet, claims victim": func(t *testing.T, f registrationFixture) RegistrationProof {
			attacker := newRegistrationFixture(t)
			batch := attacker.batch(t)
			p := attacker.proof(batch, attacker.sign(t, r, attacker.rootKey, attacker.user, batch))
			p.UserAddress = f.user
			return p
		},
		"coordinates differ from calldata": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			p := f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
			other := newRegistrationFixture(t)
			p.PublicKeyX, p.PublicKeyY = other.x.String(), other.y.String()
			return p
		},
		"wrong root signature": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			stranger, _ := crypto.GenerateKey()
			return f.proof(batch, f.sign(t, r, stranger, f.user, batch))
		},
		"signature over a different batch": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			sig := f.sign(t, r, f.rootKey, f.user, batch)
			batch.Nonce = "1"
			return f.proof(batch, sig)
		},
		"non-root key hash": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			batch.KeyHash = ComputeP256KeyHash(f.x, f.y).Hex()
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
		"webauthn signature type": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			p := f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
			p.SignatureType = "webauthn"
			return p
		},
		"register call targets another address": func(t *testing.T, f registrationFixture) RegistrationProof {
			data, _ := EncodeWebAuthnRegisterCalldata(f.x, f.y)
			batch := f.batch(t, apiresp.CaliburCall{To: testToken, Value: "0", Data: hexutil.Encode(data)})
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
		"register call carries value": func(t *testing.T, f registrationFixture) RegistrationProof {
			data, _ := EncodeWebAuthnRegisterCalldata(f.x, f.y)
			batch := f.batch(t, apiresp.CaliburCall{To: f.user.Hex(), Value: "1", Data: hexutil.Encode(data)})
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
		"extra call in batch": func(t *testing.T, f registrationFixture) RegistrationProof {
			data, _ := EncodeWebAuthnRegisterCalldata(f.x, f.y)
			call := apiresp.CaliburCall{To: f.user.Hex(), Value: "0", Data: hexutil.Encode(data)}
			batch := f.batch(t, call, apiresp.CaliburCall{To: testToken, Value: "0", Data: "0x"})
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
		"batch tolerates a failed call": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			batch.BatchedCall.RevertOnFailure = false
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
		"malformed batch": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			sig := f.sign(t, r, f.rootKey, f.user, batch)
			batch.KeyHash = "0x00"
			return f.proof(batch, sig)
		},
		"unwrapped signature": func(t *testing.T, f registrationFixture) RegistrationProof {
			batch := f.batch(t)
			wrapped := f.sign(t, r, f.rootKey, f.user, batch)
			values, err := wrappedSigArgs.Unpack(wrapped)
			if err != nil {
				t.Fatalf("unpack signature: %v", err)
			}
			sig, ok := values[0].([]byte)
			if !ok {
				t.Fatalf("signature type = %T, want []byte", values[0])
			}
			return f.proof(batch, sig)
		},
		"point not on curve": func(t *testing.T, f registrationFixture) RegistrationProof {
			f.y = new(big.Int).Add(f.y, big.NewInt(1))
			batch := f.batch(t)
			return f.proof(batch, f.sign(t, r, f.rootKey, f.user, batch))
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := r.VerifyRegistration(build(t, newRegistrationFixture(t)))
			if !errors.Is(err, ErrInvalidRegistration) {
				t.Fatalf("err = %v, want ErrInvalidRegistration", err)
			}
		})
	}
}
