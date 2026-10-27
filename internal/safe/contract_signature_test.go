package safe

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// contractSig is a standalone contract signature as clc-app submits it and the
// Transaction Service stores it: r = owner, s = 65, v = 0, length, data.
func contractSig(owner common.Address, data []byte) []byte {
	sig := common.LeftPadBytes(owner[:], 32)
	sig = append(sig, common.LeftPadBytes([]byte{65}, 32)...)
	sig = append(sig, 0)
	sig = append(sig, common.LeftPadBytes(big.NewInt(int64(len(data))).Bytes(), 32)...)
	return append(sig, data...)
}

func TestParseContractSignature(t *testing.T) {
	owner := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	data := bytes.Repeat([]byte{0x5a}, 70)

	got, err := ParseContractSignature(contractSig(owner, data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != owner || !bytes.Equal(got.Data, data) {
		t.Fatalf("got %s %x", got.Owner.Hex(), got.Data)
	}
	padded, err := ParseContractSignature(append(contractSig(owner, data), make([]byte, 26)...))
	if err != nil || !bytes.Equal(padded.Data, data) {
		t.Fatalf("word padding: %v", err)
	}

	for name, mutate := range map[string]func([]byte) []byte{
		"ECDSA v":             func(s []byte) []byte { s[64] = 27; return s },
		"dirty owner word":    func(s []byte) []byte { s[0] = 1; return s },
		"offset not 65":       func(s []byte) []byte { s[63] = 66; return s },
		"zero length":         func(s []byte) []byte { return contractSig(owner, nil) },
		"length past the end": func(s []byte) []byte { return s[:len(s)-1] },
		"trailing data":       func(s []byte) []byte { return append(s, 1) },
		"a word of padding":   func(s []byte) []byte { return append(s, make([]byte, 32)...) },
		"too short":           func(s []byte) []byte { return s[:96] },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseContractSignature(mutate(contractSig(owner, data))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSupportsContractSignatures(t *testing.T) {
	for version, want := range map[string]bool{
		"1.5.0": true, "1.5.0+L2": true, "1.6.0": true, "2.0.0": true,
		"1.4.1": false, "1.3.0": false, "1.1.1": false, "": false, "custom": false, "1": false,
	} {
		if got := SupportsContractSignatures(version); got != want {
			t.Errorf("%q = %v, want %v", version, got, want)
		}
	}
}

func TestContractVerifierCalls(t *testing.T) {
	safeAddress := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	owner := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	hash := common.HexToHash("0x" + "cd" + "00000000000000000000000000000000000000000000000000000000000000")
	data := []byte{1, 2, 3}
	ctx := context.Background()
	boom := errors.New("dial tcp")
	reverted := errors.New("execution reverted")

	versionOut := func(v string) []byte {
		out, err := abi.Arguments{{Type: mustType(t, "string")}}.Pack(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for name, tc := range map[string]struct {
		out  []byte
		err  error
		want bool
		fail bool
	}{
		"1.5.0":       {out: versionOut("1.5.0"), want: true},
		"1.4.1":       {out: versionOut("1.4.1")},
		"no VERSION":  {err: reverted},
		"garbage":     {out: []byte{1}},
		"rpc failure": {err: boom, fail: true},
	} {
		t.Run("Accepts "+name, func(t *testing.T) {
			v := NewContractVerifier(safeAddress, func(_ context.Context, to common.Address, input []byte) ([]byte, error) {
				if to != safeAddress || !bytes.Equal(input, versionFunc.Selector[:]) {
					t.Fatalf("called %s with %x", to.Hex(), input)
				}
				return tc.out, tc.err
			})
			got, err := v.Accepts(ctx)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("Accepts() = %v, %v", got, err)
			}
		})
	}

	magic := append(hexutil.MustDecode("0x1626ba7e"), make([]byte, 28)...)
	for name, tc := range map[string]struct {
		out  []byte
		err  error
		want bool
		fail bool
	}{
		"magic value":        {out: magic, want: true},
		"legacy magic value": {out: append(hexutil.MustDecode("0x20c13b0b"), make([]byte, 28)...)},
		"invalid value":      {out: append(hexutil.MustDecode("0xffffffff"), make([]byte, 28)...)},
		"owner without code": {out: nil},
		"reverts":            {err: reverted},
		"rpc failure":        {err: boom, fail: true},
	} {
		t.Run("Valid "+name, func(t *testing.T) {
			want, err := isValidSignatureFunc.EncodeArgs(hash, data)
			if err != nil || !bytes.Equal(want[:4], hexutil.MustDecode("0x1626ba7e")) {
				t.Fatalf("isValidSignature(bytes32,bytes) calldata %x, %v", want, err)
			}
			v := NewContractVerifier(safeAddress, func(_ context.Context, to common.Address, input []byte) ([]byte, error) {
				if to != owner || !bytes.Equal(input, want) {
					t.Fatalf("called %s with %x", to.Hex(), input)
				}
				return tc.out, tc.err
			})
			got, err := v.Valid(ctx, owner, hash, data)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("Valid() = %v, %v", got, err)
			}
		})
	}
}

// The vector clc-app's caliburNestedPersonalSignDigest test pins; the fork test
// proves this formula against the deployed Calibur.
func TestNestedPersonalSignDigestVector(t *testing.T) {
	got := nestedPersonalSignDigest(
		common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
	)
	if want := common.HexToHash("0x91bdbc83f28b75d3df4fac8452de56db0c16cc019f4209875dbca3048d0c160f"); got != want {
		t.Fatalf("digest %s, want %s", got.Hex(), want.Hex())
	}
}
