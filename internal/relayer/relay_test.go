package relayer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/relayer/testsign"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

const (
	testChainID = 42220
	testCalibur = "0x1289fE793392fb1a905f78Ea23f632EDD3d15b85"
	testToken   = "0x765DE816845861e75A25fCA122bb6898B8B1282a"
	testSpender = "0x204653A89FF5F2A935c88b0c750cAcdaA9e7368d"
	testUser    = "0x1234567890123456789012345678901234567890"
)

type (
	stubOracle struct{}
)

func (stubOracle) GetSettings() (*gas.GasSettings, error) {
	return &gas.GasSettings{GasFeeCap: big.NewInt(50_000_000_000), GasTipCap: big.NewInt(1_000_000_000)}, nil
}
func (stubOracle) Start() {}
func (stubOracle) Stop()  {}

func testRelayer() *Relayer {
	return &Relayer{
		gasOracle:             stubOracle{},
		chainID:               big.NewInt(testChainID),
		caliburAddress:        common.HexToAddress(testCalibur),
		perCallGas:            80_000,
		baseExecGas:           50_000,
		delegationOverheadGas: 150_000,
		maxRelayGas:           3_000_000,
		webAuthnVerifyGas:     400_000,
	}
}

// padArg left-pads a 32-byte ABI word.
func padArg(b []byte) []byte { return common.LeftPadBytes(b, 32) }

// approveTransferBatch builds the canonical approve0 + approve + transferFrom
// batch used across these tests. It is deterministic so the digest is golden.
func approveTransferBatch() apiresp.CaliburBatchedCall {
	spender := common.HexToAddress(testSpender)
	user := common.HexToAddress(testUser)
	amt := padArg(big.NewInt(1000).Bytes())

	approve := append(common.FromHex("0x095ea7b3"), padArg(spender.Bytes())...)
	approve = append(approve, amt...)

	transfer := append(common.FromHex("0x23b872dd"), padArg(user.Bytes())...)
	transfer = append(transfer, padArg(spender.Bytes())...)
	transfer = append(transfer, amt...)

	return apiresp.CaliburBatchedCall{
		Calls: []apiresp.CaliburCall{
			{To: testToken, Value: "0", Data: hexutil.Encode(approve)},
			{To: testToken, Value: "0", Data: hexutil.Encode(approve)},
			{To: testToken, Value: "0", Data: hexutil.Encode(transfer)},
		},
		RevertOnFailure: true,
	}
}

// TestGoldenDigest pins the EIP-712 digest over a 3-call batch. It is produced
// by gocalibur.ComputeDigest, which uses the identical EIP-712 domain, type
// strings, and message layout as the dapp's computeEIP712Digest
// (clc-app .../webauthn/logic.ts), so this value also locks Go<->TS agreement.
func TestGoldenDigest(t *testing.T) {
	const golden = "0xfa9285a7dfd50576eeaa48443fea0e576eacb4ff703a2d0dd366952d8937b771"

	signed, err := convertSignedBatch(apiresp.CaliburSignedBatch{
		BatchedCall: approveTransferBatch(),
		Nonce:       "7",
		KeyHash:     common.Hash{}.Hex(),
		Executor:    common.Address{}.Hex(),
		Deadline:    "1893456000",
	})
	if err != nil {
		t.Fatalf("convertSignedBatch: %v", err)
	}

	domain := gocalibur.CaliburDomain(big.NewInt(testChainID), common.HexToAddress(testUser), common.HexToAddress(testCalibur))
	d := gocalibur.ComputeDigest(domain, signed)
	if got := hexutil.Encode(d[:]); got != golden {
		t.Fatalf("digest = %s, want %s (Go<->TS EIP-712 divergence?)", got, golden)
	}
}

func TestRelayGasLimit(t *testing.T) {
	r := testRelayer()

	// delegated secp256k1: (50_000 + 80_000*3) * 1.10 = 319_000
	if got := r.relayGasLimit(3, true, false); got != 319_000 {
		t.Fatalf("delegated 3-call gas = %d, want 319000", got)
	}
	// first relay adds delegation overhead: (50_000 + 240_000 + 150_000)*1.10 = 484_000
	if got := r.relayGasLimit(3, false, false); got != 484_000 {
		t.Fatalf("first-relay 3-call gas = %d, want 484000", got)
	}
	// delegated webauthn adds verify overhead: (50_000 + 240_000 + 400_000)*1.10 = 759_000
	if got := r.relayGasLimit(3, true, true); got != 759_000 {
		t.Fatalf("delegated webauthn 3-call gas = %d, want 759000", got)
	}
	// clamp
	r.maxRelayGas = 100_000
	if got := r.relayGasLimit(16, false, false); got != 100_000 {
		t.Fatalf("clamped gas = %d, want 100000", got)
	}
}

// A one-call relay has to cover the heaviest single action we sign, which is a
// first GiftableToken mint: 103_407 measured on Celo (three zero->nonzero
// SSTOREs — recipient balance, totalSupply, totalMinted). Of the tx limit,
// ~26_000 goes to intrinsic + calldata and ~41_000 to Calibur's decode,
// ecrecover and nonce write before the token frame is entered, so the limit
// must clear roughly 170_000 for the mint to land. per_call_gas = 80_000 gave
// 143_000 and reverted with OutOfGas.
func TestRelayGasLimitCoversFirstMint(t *testing.T) {
	const (
		firstMintGas    = 103_407
		relayOverhead   = 67_000
		productionLimit = 275_000 // (50_000 + 200_000) * 1.10
	)

	r := testRelayer()
	r.perCallGas = 200_000

	got := r.relayGasLimit(1, true, false)
	if got != productionLimit {
		t.Fatalf("one-call delegated gas = %d, want %d", got, productionLimit)
	}
	if got < firstMintGas+relayOverhead {
		t.Fatalf("one-call gas = %d, below the %d a first mint needs", got, firstMintGas+relayOverhead)
	}

	// The regression: the previous default could not fund the mint.
	r.perCallGas = 80_000
	if old := r.relayGasLimit(1, true, false); old >= firstMintGas+relayOverhead {
		t.Fatalf("80k per-call budget = %d, expected it to fall short of a first mint", old)
	}
}

func TestBuildCaliburNonce(t *testing.T) {
	// root key (keyHash 0) => nonce == seq
	if got := buildCaliburNonce(common.Hash{}, 42); got.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("root nonce = %s, want 42", got)
	}
	// non-root: keyhash prefix shifted left 64, seq in low bits
	var kh common.Hash
	kh[0] = 0xAB
	got := buildCaliburNonce(kh, 5)
	low := new(big.Int).And(got, new(big.Int).SetUint64(^uint64(0)))
	if low.Uint64() != 5 {
		t.Fatalf("low 64 bits = %d, want 5", low.Uint64())
	}
}

func TestBuildAndSignRelay_Secp256k1_Delegated(t *testing.T) {
	r := testRelayer()
	masterKey, _ := crypto.GenerateKey()
	signerAddr := crypto.PubkeyToAddress(masterKey.PublicKey)
	userKey, _ := crypto.GenerateKey()

	relay, err := testsign.Secp256k1(userKey, testsign.Params{
		ChainID:  big.NewInt(testChainID),
		Calibur:  common.HexToAddress(testCalibur),
		User:     common.HexToAddress(testUser),
		Batch:    approveTransferBatch(),
		Seq:      0,
		Deadline: big.NewInt(1893456000),
	})
	if err != nil {
		t.Fatalf("testsign.Secp256k1: %v", err)
	}

	res, err := r.BuildAndSignRelay(
		context.Background(), masterKey, signerAddr,
		common.HexToAddress(testUser), relay.SignedBatch,
		common.FromHex(relay.Signature), "secp256k1", nil, true, 11,
	)
	if err != nil {
		t.Fatalf("BuildAndSignRelay: %v", err)
	}

	if res.OTXType != "RELAY_EXEC" {
		t.Fatalf("OTXType = %s, want RELAY_EXEC", res.OTXType)
	}
	if res.Tx.Type() != types.DynamicFeeTxType {
		t.Fatalf("tx type = %d, want DynamicFeeTx", res.Tx.Type())
	}
	if res.Tx.Gas() != r.relayGasLimit(3, true, false) {
		t.Fatalf("gas = %d, want %d", res.Tx.Gas(), r.relayGasLimit(3, true, false))
	}
	if res.Tx.To() == nil || *res.Tx.To() != common.HexToAddress(testUser) {
		t.Fatalf("tx.To = %v, want user", res.Tx.To())
	}
	sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(testChainID)), res.Tx)
	if err != nil || sender != signerAddr {
		t.Fatalf("recovered sender = %v (err %v), want relayer %v", sender, err, signerAddr)
	}
}

func TestBuildAndSignRelay_WebAuthn_FirstRelay(t *testing.T) {
	r := testRelayer()
	masterKey, _ := crypto.GenerateKey()
	signerAddr := crypto.PubkeyToAddress(masterKey.PublicKey)

	passkey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen p256: %v", err)
	}
	relay, err := testsign.WebAuthn(passkey, "localhost", "http://localhost:3000", testsign.Params{
		ChainID:  big.NewInt(testChainID),
		Calibur:  common.HexToAddress(testCalibur),
		User:     common.HexToAddress(testUser),
		Batch:    approveTransferBatch(),
		Seq:      0,
		Deadline: big.NewInt(1893456000),
	})
	if err != nil {
		t.Fatalf("testsign.WebAuthn: %v", err)
	}
	if relay.SignatureType != "webauthn" || relay.KeyHash == "" {
		t.Fatalf("unexpected webauthn relay: %+v", relay)
	}

	auth := &apiresp.EIP7702Authorization{
		ChainID:         "42220",
		ContractAddress: common.HexToAddress(testCalibur).Hex(),
		Nonce:           "0",
		YParity:         0,
		R:               "0x1111111111111111111111111111111111111111111111111111111111111111",
		S:               "0x2222222222222222222222222222222222222222222222222222222222222222",
	}

	res, err := r.BuildAndSignRelay(
		context.Background(), masterKey, signerAddr,
		common.HexToAddress(testUser), relay.SignedBatch,
		common.FromHex(relay.Signature), "webauthn", auth, false, 12,
	)
	if err != nil {
		t.Fatalf("BuildAndSignRelay (setcode): %v", err)
	}
	if res.OTXType != "RELAY_DELEGATE_EXEC" {
		t.Fatalf("OTXType = %s, want RELAY_DELEGATE_EXEC", res.OTXType)
	}
	if res.Tx.Type() != types.SetCodeTxType {
		t.Fatalf("tx type = %d, want SetCodeTx", res.Tx.Type())
	}
	if res.Tx.Gas() != r.relayGasLimit(3, false, true) {
		t.Fatalf("gas = %d, want %d", res.Tx.Gas(), r.relayGasLimit(3, false, true))
	}
	if len(res.Tx.SetCodeAuthorizations()) != 1 {
		t.Fatalf("auth list len = %d, want 1", len(res.Tx.SetCodeAuthorizations()))
	}
}
