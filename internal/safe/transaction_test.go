package safe

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

func fields(to common.Address, data []byte, op uint8, nonce string) TxFields {
	return TxFields{
		To: to.Hex(), Value: "0", Data: hexutil.Encode(data), Operation: op,
		SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: common.Address{}.Hex(), RefundReceiver: common.Address{}.Hex(), Nonce: nonce,
	}
}

// The expected digest is viem's hashTypedData over the same SafeTx, the
// implementation packages/core signs with, so a client and this server agree.
func TestSafeTxHashMatchesViem(t *testing.T) {
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	got, err := SafeTxHash(100, common.HexToAddress("0x1111111111111111111111111111111111111111"),
		fields(to, []byte{0xf2, 0xfd, 0xe3, 0x8b}, 0, "7"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "0x372cf8a80b6a22cff9f32815d0f603a92a9be22c92298ad600537e08dd844088"; got.Hex() != want {
		t.Fatalf("hash %s, want %s", got.Hex(), want)
	}
}

func TestSafeTxHashBindsChainSafeAndNonce(t *testing.T) {
	f := fields(assetAddr, []byte{1, 2, 3, 4}, 0, "1")
	base, _ := SafeTxHash(100, safeAddr, f)
	other := fields(assetAddr, []byte{1, 2, 3, 4}, 0, "2")
	for name, h := range map[string]func() (common.Hash, error){
		"chain": func() (common.Hash, error) { return SafeTxHash(1, safeAddr, f) },
		"safe":  func() (common.Hash, error) { return SafeTxHash(100, assetAddr, f) },
		"nonce": func() (common.Hash, error) { return SafeTxHash(100, safeAddr, other) },
	} {
		got, err := h()
		if err != nil || got == base {
			t.Errorf("%s did not change the hash (%v)", name, err)
		}
	}
	bad := f
	bad.Value = "-1"
	if _, err := SafeTxHash(100, safeAddr, bad); err == nil {
		t.Fatal("negative value accepted")
	}
}

func TestRecoverSigner(t *testing.T) {
	key, _ := crypto.GenerateKey()
	hash := common.HexToHash("0xabcdef")
	sig, _ := crypto.Sign(hash[:], key)
	sig[64] += 27
	got, err := RecoverSigner(hash, sig)
	if err != nil || got != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatalf("got %s, %v", got, err)
	}
	other, _ := RecoverSigner(common.HexToHash("0x01"), sig)
	if other == got {
		t.Fatal("signature recovered the same signer for a different hash")
	}
	for _, bad := range [][]byte{sig[:64], append(append([]byte{}, sig[:64]...), 0), append(append([]byte{}, sig[:64]...), 31)} {
		if _, err := RecoverSigner(hash, bad); err == nil {
			t.Fatal("malformed signature accepted")
		}
	}
}

func TestCheckTransactionSharesTheRelayRules(t *testing.T) {
	p := newPolicy()
	ok := fields(assetAddr, transferOwnership(holder), 0, "3")
	hostile := map[string]TxFields{
		"stranger asset":         fields(strangerAddr, transferOwnership(holder), 0, "3"),
		"arbitrary delegatecall": fields(strangerAddr, transferOwnership(holder), 1, "3"),
		"bad selector":           fields(assetAddr, []byte{0xde, 0xad, 0xbe, 0xef}, 0, "3"),
		"owner change":           fields(safeAddr, append(func() []byte { s := selector("addOwnerWithThreshold(address,uint256)"); return s[:] }(), make([]byte, 64)...), 0, "3"),
	}
	gas := ok
	gas.GasPrice = "9"
	hostile["gas refund"] = gas

	exec, _ := ok.Exec()
	if err := p.CheckTransaction(context.Background(), safeAddr, exec); err != nil {
		t.Fatal(err)
	}
	cancel, _ := fields(safeAddr, nil, 0, "3").Exec()
	if err := p.CheckTransaction(context.Background(), safeAddr, cancel); err != nil {
		t.Fatalf("cancellation rejected: %v", err)
	}
	for name, f := range hostile {
		e, err := f.Exec()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.CheckTransaction(context.Background(), safeAddr, e); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: got %v, want ErrRejected", name, err)
		}
	}
}
