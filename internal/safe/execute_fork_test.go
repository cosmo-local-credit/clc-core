package safe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"github.com/lmittmann/w3"
	"github.com/stretchr/testify/require"
)

// Execution against real Safe contracts on a Gnosis fork:
//
//	anvil --fork-url <gnosis rpc> --port 8547 --hardfork prague
//	SAFE_FORK_RPC=http://127.0.0.1:8547 go test ./internal/safe -run RealSafes
//
// A third party (anvil's first account, standing in for clc-core's system
// signer) executes with signatures packed by PackSignatures.

var (
	createProxyFunc   = w3.MustNewFunc("createProxyWithNonce(address,bytes,uint256)", "address")
	setupFunc         = w3.MustNewFunc("setup(address[],uint256,address,bytes,address,address,uint256,address)", "")
	getTxHashFunc     = w3.MustNewFunc("getTransactionHash(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256)", "bytes32")
	forkNonceFunc     = w3.MustNewFunc("nonce()", "uint256")
	anvilFirstAccount = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

	forkSafeVersions = []forkSafeVersion{
		{"1.3.0", "0x3E5c63644E683549055b9Be8653de26E0B4CD36E", "0xa6B71E26C5e0845f74c812102Ca7114b6a896AB2"},
		{"1.5.0", "0xEdd160fEBBD92E350D4D398fb636302fccd67C7e", "0x14F2982D601c9458F93bd70B218933A6f8165e7b"},
	}
	// The Calibur implementation clc-app delegates to on Gnosis.
	forkCalibur = common.HexToAddress("0x48A910C8e9FF0b14051b78d0c96dB069E57f0729")
)

func TestExecutionAgainstRealSafes(t *testing.T) {
	rpcURL := os.Getenv("SAFE_FORK_RPC")
	if rpcURL == "" {
		t.Skip("SAFE_FORK_RPC not set; run anvil --fork-url <gnosis rpc>")
	}
	ctx := context.Background()
	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, chainID.Int64(), "fork Gnosis")
	payer, err := crypto.HexToECDSA(anvilFirstAccount)
	require.NoError(t, err)

	for _, version := range forkSafeVersions {
		t.Run(version.name, func(t *testing.T) {
			owners := newOwners(t, 3)
			proxy := deployForkSafe(t, client, payer, version, addresses(owners), 2)

			fields := forkNoopFields(owners[2].addr)
			hash, err := SafeTxHash(100, proxy, fields)
			require.NoError(t, err)
			exec, err := fields.Exec()
			require.NoError(t, err)
			requireOnchainHash(t, client, proxy, exec, hash)

			// One typed-data and one eth_sign confirmation, as the Safe app
			// and wallets produce them.
			exec.Signatures, err = PackSignatures(ctx, hash, []Confirmation{
				typedSig(t, owners[0], hash), ethSignSig(t, owners[1], hash),
			}, addresses(owners), 2, nil)
			require.NoError(t, err)
			calldata, err := EncodeExecTransaction(exec)
			require.NoError(t, err)
			receipt := sendFork(t, client, payer, proxy, calldata)
			require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)

			require.EqualValues(t, 1, forkSafeNonce(t, client, proxy), "the Safe executed nonce 0")
		})
	}
}

// A Calibur passkey account confirms with a contract signature: its passkey
// signs the ERC-7739 nested PersonalSign of the safeTxHash, and Safe 1.5.0
// verifies it through the account's isValidSignature(bytes32,bytes). Safe
// 1.3.0 calls the legacy isValidSignature(bytes,bytes), so it is refused there.
func TestPasskeyContractSignatureAgainstRealSafes(t *testing.T) {
	rpcURL := os.Getenv("SAFE_FORK_RPC")
	if rpcURL == "" {
		t.Skip("SAFE_FORK_RPC not set; run anvil --fork-url <gnosis rpc> --hardfork prague")
	}
	ctx := context.Background()
	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)
	payer, err := crypto.HexToECDSA(anvilFirstAccount)
	require.NoError(t, err)

	account, passkey := delegatedPasskeyAccount(t, client)
	call := func(ctx context.Context, to common.Address, data []byte) ([]byte, error) {
		return client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	}
	out, err := call(ctx, account, crypto.Keccak256([]byte("domainSeparator()"))[:4])
	require.NoError(t, err)
	domainSeparator := common.BytesToHash(out)

	for _, version := range forkSafeVersions {
		t.Run(version.name, func(t *testing.T) {
			ecdsaOwner := newOwners(t, 1)[0]
			owners := []common.Address{ecdsaOwner.addr, account}
			proxy := deployForkSafe(t, client, payer, version, owners, 2)

			fields := forkNoopFields(ecdsaOwner.addr)
			hash, err := SafeTxHash(100, proxy, fields)
			require.NoError(t, err)
			exec, err := fields.Exec()
			require.NoError(t, err)
			requireOnchainHash(t, client, proxy, exec, hash)

			digest := nestedPersonalSignDigest(domainSeparator, hash)
			payload := gocalibur.WrapSignatureWithKeyHash(passkeyKeyHash(passkey), signDigestWithPasskey(t, passkey, digest), nil)
			confirmations := []Confirmation{typedSig(t, ecdsaOwner, hash), contractConfirmation(account, payload)}
			verifier := NewContractVerifier(proxy, call)

			// Valid for the account whatever the Safe version.
			valid, err := verifier.Valid(ctx, account, hash, payload)
			require.NoError(t, err)
			require.True(t, valid, "Calibur accepts the nested PersonalSign")

			if version.name != "1.5.0" {
				_, err := PackSignatures(ctx, hash, confirmations, owners, 2, verifier)
				require.ErrorIs(t, err, ErrNotReady, "the version gate refuses Safe %s", version.name)
				// Without the gate the Safe itself would reject the signature.
				exec.Signatures, err = PackSignatures(ctx, hash, confirmations, owners, 2, acceptingVerifier{verifier})
				require.NoError(t, err)
				calldata, err := EncodeExecTransaction(exec)
				require.NoError(t, err)
				_, err = client.EstimateGas(ctx, ethereum.CallMsg{From: crypto.PubkeyToAddress(payer.PublicKey), To: &proxy, Data: calldata})
				require.Error(t, err, "Safe %s cannot verify a Calibur contract signature", version.name)
				return
			}

			exec.Signatures, err = PackSignatures(ctx, hash, confirmations, owners, 2, verifier)
			require.NoError(t, err)
			calldata, err := EncodeExecTransaction(exec)
			require.NoError(t, err)
			receipt := sendFork(t, client, payer, proxy, calldata)
			require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)
			require.EqualValues(t, 1, forkSafeNonce(t, client, proxy), "the Safe executed nonce 0")
			t.Logf("passkey contract signature executed with %d gas", receipt.GasUsed)
		})
	}
}

type (
	forkSafeVersion struct{ name, singleton, factory string }

	// acceptingVerifier skips the version gate, to show what it guards.
	acceptingVerifier struct{ ContractVerifier }
)

func (acceptingVerifier) Accepts(context.Context) (bool, error) { return true, nil }

func deployForkSafe(t *testing.T, client *ethclient.Client, payer *ecdsa.PrivateKey, version forkSafeVersion, owners []common.Address, threshold int64) common.Address {
	t.Helper()
	ctx := context.Background()
	setup, err := setupFunc.EncodeArgs(owners, big.NewInt(threshold), common.Address{}, []byte{},
		common.Address{}, common.Address{}, big.NewInt(0), common.Address{})
	require.NoError(t, err)
	create, err := createProxyFunc.EncodeArgs(common.HexToAddress(version.singleton), setup, big.NewInt(time.Now().UnixNano()))
	require.NoError(t, err)
	factory := common.HexToAddress(version.factory)
	out, err := client.CallContract(ctx, ethereum.CallMsg{From: crypto.PubkeyToAddress(payer.PublicKey), To: &factory, Data: create}, nil)
	require.NoError(t, err)
	var proxy common.Address
	require.NoError(t, createProxyFunc.DecodeReturns(out, &proxy))
	sendFork(t, client, payer, factory, create)
	return proxy
}

func forkNoopFields(to common.Address) TxFields {
	return TxFields{
		To: to.Hex(), Value: "0", Data: "0x", Operation: 0,
		SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: common.Address{}.Hex(), RefundReceiver: common.Address{}.Hex(), Nonce: "0",
	}
}

func requireOnchainHash(t *testing.T, client *ethclient.Client, proxy common.Address, exec *Exec, hash common.Hash) {
	t.Helper()
	query, err := getTxHashFunc.EncodeArgs(exec.To, exec.Value, exec.Data, exec.Operation, exec.SafeTxGas,
		exec.BaseGas, exec.GasPrice, exec.GasToken, exec.RefundReceiver, big.NewInt(0))
	require.NoError(t, err)
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &proxy, Data: query}, nil)
	require.NoError(t, err)
	var onchain common.Hash
	require.NoError(t, getTxHashFunc.DecodeReturns(out, &onchain))
	require.Equal(t, onchain, hash, "our SafeTx digest must be the Safe's own")
}

func forkSafeNonce(t *testing.T, client *ethclient.Client, proxy common.Address) int64 {
	t.Helper()
	nonceCall, err := forkNonceFunc.EncodeArgs()
	require.NoError(t, err)
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &proxy, Data: nonceCall}, nil)
	require.NoError(t, err)
	var nonce big.Int
	require.NoError(t, forkNonceFunc.DecodeReturns(out, &nonce))
	return nonce.Int64()
}

// delegatedPasskeyAccount delegates a fresh EOA to Calibur and registers a
// WebAuthn P-256 passkey on it, in one self-sponsored SetCode transaction, as
// clc-app's onboarding does through the relayer.
func delegatedPasskeyAccount(t *testing.T, client *ethclient.Client) (common.Address, *ecdsa.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	user, err := crypto.GenerateKey()
	require.NoError(t, err)
	passkey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	account := crypto.PubkeyToAddress(user.PublicKey)
	require.NoError(t, client.Client().CallContext(ctx, nil, "anvil_setBalance", account, "0xde0b6b3a7640000"))

	// The sender's nonce is spent before the authorization is applied.
	auth, err := gocalibur.CreateAndSignAuthorization(user, big.NewInt(100), forkCalibur, 1)
	require.NoError(t, err)
	tip, err := client.SuggestGasTipCap(ctx)
	require.NoError(t, err)
	head, err := client.HeaderByNumber(ctx, nil)
	require.NoError(t, err)
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)
	signed, err := types.SignTx(types.NewTx(&types.SetCodeTx{
		ChainID: uint256.NewInt(100), Nonce: 0,
		GasTipCap: uint256.MustFromBig(tip), GasFeeCap: uint256.MustFromBig(feeCap),
		Gas: 500_000, To: account, Value: uint256.NewInt(0),
		Data: registerPasskeyCalldata(t, passkey), AuthList: []types.SetCodeAuthorization{auth},
	}), types.LatestSignerForChainID(big.NewInt(100)), user)
	require.NoError(t, err)
	require.NoError(t, client.SendTransaction(ctx, signed))
	receipt := waitForkReceipt(t, client, signed.Hash())
	require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status, "delegate and register")

	code, err := client.CodeAt(ctx, account, nil)
	require.NoError(t, err)
	require.True(t, gocalibur.IsDelegatedTo(code, forkCalibur), "account delegates to Calibur")
	return account, passkey
}

// registerPasskeyCalldata is Calibur register((uint8,bytes)) for a WebAuthn
// P-256 key, as clc-app's encodeRegisterCalldata builds it.
func registerPasskeyCalldata(t *testing.T, passkey *ecdsa.PrivateKey) []byte {
	t.Helper()
	tuple, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{Name: "keyType", Type: "uint8"},
		{Name: "publicKey", Type: "bytes"},
	})
	require.NoError(t, err)
	x, y := passkeyXY(passkey)
	packed, err := abi.Arguments{{Type: tuple}}.Pack(struct {
		KeyType   uint8
		PublicKey []byte
	}{KeyType: 1, PublicKey: append(common.LeftPadBytes(x.Bytes(), 32), common.LeftPadBytes(y.Bytes(), 32)...)})
	require.NoError(t, err)
	return append(crypto.Keccak256([]byte("register((uint8,bytes))"))[:4], packed...)
}

func passkeyXY(passkey *ecdsa.PrivateKey) (*big.Int, *big.Int) {
	//nolint:staticcheck // gocalibur takes the legacy *big.Int coordinates
	return passkey.PublicKey.X, passkey.PublicKey.Y
}

func passkeyKeyHash(passkey *ecdsa.PrivateKey) [32]byte {
	x, y := passkeyXY(passkey)
	return gocalibur.NewWebAuthnP256Key(x, y).KeyHash()
}

// nestedPersonalSignDigest is what a Calibur key signs for isValidSignature
// under ERC-7739: PersonalSign(bytes prefixed) over hash, in the account's
// own EIP-712 domain.
func nestedPersonalSignDigest(domainSeparator, hash common.Hash) common.Hash {
	typeHash := crypto.Keccak256([]byte("PersonalSign(bytes prefixed)"))
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainSeparator[:], crypto.Keccak256(typeHash, hash[:]))
}

// signDigestWithPasskey is a browser assertion over digest, encoded as
// Calibur's WebAuthnAuth.
func signDigestWithPasskey(t *testing.T, passkey *ecdsa.PrivateKey, digest common.Hash) []byte {
	t.Helper()
	clientDataJSON := fmt.Sprintf(`{"type":"webauthn.get","challenge":"%s","origin":"https://test.cosmolocal.credit","crossOrigin":false}`,
		base64.RawURLEncoding.EncodeToString(digest[:]))
	rpIDHash := sha256.Sum256([]byte("test.cosmolocal.credit"))
	authData := append(rpIDHash[:], 0x05, 0, 0, 0, 1) // UP | UV, signCount 1
	clientDataHash := sha256.Sum256([]byte(clientDataJSON))
	r, s, err := gocalibur.SignP256(passkey, append(slices.Clone(authData), clientDataHash[:]...))
	require.NoError(t, err)
	auth, err := gocalibur.ParseWebAuthnAuthFromBrowser(authData, clientDataJSON, r, s)
	require.NoError(t, err)
	encoded, err := gocalibur.EncodeWebAuthnAuth(auth)
	require.NoError(t, err)
	return encoded
}

func waitForkReceipt(t *testing.T, client *ethclient.Client, hash common.Hash) *types.Receipt {
	t.Helper()
	for range 50 {
		receipt, err := client.TransactionReceipt(context.Background(), hash)
		if err == nil {
			return receipt
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no receipt for %s", hash.Hex())
	return nil
}

func sendFork(t *testing.T, client *ethclient.Client, key *ecdsa.PrivateKey, to common.Address, data []byte) *types.Receipt {
	t.Helper()
	ctx := context.Background()
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	require.NoError(t, err)
	gas, err := client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Data: data})
	require.NoError(t, err)
	tip, err := client.SuggestGasTipCap(ctx)
	require.NoError(t, err)
	head, err := client.HeaderByNumber(ctx, nil)
	require.NoError(t, err)
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)
	signed, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(100), Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap,
		Gas: gas + gas/4, To: &to, Data: data,
	}), types.LatestSignerForChainID(big.NewInt(100)), key)
	require.NoError(t, err)
	require.NoError(t, client.SendTransaction(ctx, signed))
	return waitForkReceipt(t, client, signed.Hash())
}
