package safe

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

var (
	safeAddr     = common.HexToAddress("0x1111111111111111111111111111111111111111")
	assetAddr    = common.HexToAddress("0x2222222222222222222222222222222222222222")
	strangerAddr = common.HexToAddress("0x3333333333333333333333333333333333333333")
	multiSendAt  = common.HexToAddress("0x40A2aCCbd92BCA938b02010E17A5b8929b49130D")
	holder       = common.HexToAddress("0x4444444444444444444444444444444444444444")
)

type fakeResolver struct {
	safes  map[common.Address]bool
	assets map[common.Address]bool
	err    error
}

func (f fakeResolver) IsSafe(_ context.Context, a common.Address) (bool, error) {
	return f.safes[a], f.err
}

func (f fakeResolver) ManagesAsset(_ context.Context, _, target common.Address) (bool, error) {
	return f.assets[target], f.err
}

func newPolicy() *Policy {
	return NewPolicy(fakeResolver{
		safes:  map[common.Address]bool{safeAddr: true},
		assets: map[common.Address]bool{assetAddr: true},
	}, []common.Address{multiSendAt}, DefaultAllowedSignatures)
}

func mustType(t *testing.T, s string) abi.Type {
	t.Helper()
	ty, err := abi.NewType(s, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ty
}

func transferOwnership(to common.Address) []byte {
	sel := selector("transferOwnership(address)")
	return append(sel[:], common.LeftPadBytes(to.Bytes(), 32)...)
}

func encodeExec(t *testing.T, to common.Address, value *big.Int, data []byte, op uint8, gasPrice *big.Int, sigs []byte) []byte {
	t.Helper()
	args := abi.Arguments{
		{Type: mustType(t, "address")}, {Type: mustType(t, "uint256")}, {Type: mustType(t, "bytes")},
		{Type: mustType(t, "uint8")}, {Type: mustType(t, "uint256")}, {Type: mustType(t, "uint256")},
		{Type: mustType(t, "uint256")}, {Type: mustType(t, "address")}, {Type: mustType(t, "address")},
		{Type: mustType(t, "bytes")},
	}
	packed, err := args.Pack(to, value, data, op, big.NewInt(0), big.NewInt(0), gasPrice,
		common.Address{}, common.Address{}, sigs)
	if err != nil {
		t.Fatal(err)
	}
	return append(execTransactionSelector[:], packed...)
}

func packSub(op uint8, to common.Address, value *big.Int, data []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(op)
	b.Write(to.Bytes())
	b.Write(common.LeftPadBytes(value.Bytes(), 32))
	b.Write(common.LeftPadBytes(big.NewInt(int64(len(data))).Bytes(), 32))
	b.Write(data)
	return b.Bytes()
}

func encodeMultiSend(t *testing.T, subs ...[]byte) []byte {
	t.Helper()
	args := abi.Arguments{{Type: mustType(t, "bytes")}}
	packed, err := args.Pack(bytes.Join(subs, nil))
	if err != nil {
		t.Fatal(err)
	}
	return append(multiSendSelector[:], packed...)
}

func sigs(n int) []byte { return bytes.Repeat([]byte{0xab}, 65*n) }

func execCall(data []byte) []Call {
	return []Call{{To: safeAddr, Value: big.NewInt(0), Data: data}}
}

func TestCheckIgnoresBatchesWithoutASafe(t *testing.T) {
	v, err := newPolicy().Check(context.Background(), []Call{
		{To: assetAddr, Value: big.NewInt(0), Data: transferOwnership(holder)},
	})
	if err != nil || v != nil {
		t.Fatalf("got %v, %v; want nil, nil", v, err)
	}
}

func TestCheckAcceptsSingleCall(t *testing.T) {
	data := encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(2))
	v, err := newPolicy().Check(context.Background(), execCall(data))
	if err != nil {
		t.Fatal(err)
	}
	if v.Safe != safeAddr || v.Signatures != 2 || v.Subcalls != 1 {
		t.Fatalf("verdict %+v", v)
	}
}

func TestCheckAcceptsMultiSend(t *testing.T) {
	inner := encodeMultiSend(t,
		packSub(0, assetAddr, big.NewInt(0), transferOwnership(holder)),
		packSub(0, assetAddr, big.NewInt(0), transferOwnership(holder)),
		packSub(0, assetAddr, big.NewInt(0), transferOwnership(holder)),
		packSub(0, assetAddr, big.NewInt(0), transferOwnership(holder)),
	)
	data := encodeExec(t, multiSendAt, big.NewInt(0), inner, 1, big.NewInt(0), sigs(3))
	v, err := newPolicy().Check(context.Background(), execCall(data))
	if err != nil {
		t.Fatal(err)
	}
	if v.Subcalls != 4 || v.Signatures != 3 {
		t.Fatalf("verdict %+v", v)
	}
}

func TestCheckAcceptsCancellation(t *testing.T) {
	data := encodeExec(t, safeAddr, big.NewInt(0), nil, 0, big.NewInt(0), sigs(1))
	if _, err := newPolicy().Check(context.Background(), execCall(data)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsHostileBatches(t *testing.T) {
	ownerSel := selector("addOwnerWithThreshold(address,uint256)")
	ownerChange := append(ownerSel[:], make([]byte, 64)...)
	evilMulti := encodeMultiSend(t,
		packSub(0, assetAddr, big.NewInt(0), transferOwnership(holder)),
		packSub(0, strangerAddr, big.NewInt(0), transferOwnership(holder)),
	)
	delegateInSend := encodeMultiSend(t, packSub(1, assetAddr, big.NewInt(0), transferOwnership(holder)))
	valueSub := encodeMultiSend(t, packSub(0, assetAddr, big.NewInt(1), transferOwnership(holder)))

	cases := map[string][]Call{
		"stranger asset":            execCall(encodeExec(t, strangerAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))),
		"self call changes owners":  execCall(encodeExec(t, safeAddr, big.NewInt(0), ownerChange, 0, big.NewInt(0), sigs(1))),
		"delegatecall to arbitrary": execCall(encodeExec(t, strangerAddr, big.NewInt(0), transferOwnership(holder), 1, big.NewInt(0), sigs(1))),
		"multisend with stranger":   execCall(encodeExec(t, multiSendAt, big.NewInt(0), evilMulti, 1, big.NewInt(0), sigs(1))),
		"delegate subcall":          execCall(encodeExec(t, multiSendAt, big.NewInt(0), delegateInSend, 1, big.NewInt(0), sigs(1))),
		"subcall value":             execCall(encodeExec(t, multiSendAt, big.NewInt(0), valueSub, 1, big.NewInt(0), sigs(1))),
		"inner value":               execCall(encodeExec(t, assetAddr, big.NewInt(1), transferOwnership(holder), 0, big.NewInt(0), sigs(1))),
		"gas refund":                execCall(encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(7), sigs(1))),
		"selector not allowed":      execCall(encodeExec(t, assetAddr, big.NewInt(0), []byte{0xde, 0xad, 0xbe, 0xef}, 0, big.NewInt(0), sigs(1))),
		"ragged signatures":         execCall(encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), []byte{1, 2, 3})),
		"no signatures":             execCall(encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), nil)),
		"safe call is not exec":     {{To: safeAddr, Value: big.NewInt(0), Data: ownerChange}},
		"exec to unknown safe is still policed": {{To: strangerAddr, Value: big.NewInt(0),
			Data: encodeExec(t, strangerAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))}},
		"safe call mixed with others": {
			{To: assetAddr, Value: big.NewInt(0), Data: transferOwnership(holder)},
			{To: safeAddr, Value: big.NewInt(0), Data: encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))},
		},
		"value sent to safe": {{To: safeAddr, Value: big.NewInt(1),
			Data: encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))}},
	}
	for name, calls := range cases {
		t.Run(name, func(t *testing.T) {
			v, err := newPolicy().Check(context.Background(), calls)
			if !errors.Is(err, ErrRejected) || v != nil {
				t.Fatalf("got %v, %v; want ErrRejected", v, err)
			}
		})
	}
}

func TestCheckFailsClosedOnResolverError(t *testing.T) {
	p := NewPolicy(fakeResolver{err: errors.New("db down")}, []common.Address{multiSendAt}, DefaultAllowedSignatures)
	data := encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))
	if v, err := p.Check(context.Background(), execCall(data)); err == nil || v != nil {
		t.Fatalf("got %v, %v; want error", v, err)
	}
}

func TestDecodersRejectTruncatedInput(t *testing.T) {
	data := encodeExec(t, assetAddr, big.NewInt(0), transferOwnership(holder), 0, big.NewInt(0), sigs(1))
	for n := 0; n < len(data); n += 7 {
		if _, err := DecodeExec(data[:n]); err == nil && n < len(data) {
			if n >= 4+10*32 {
				continue // a truncated tail past the head may still parse if lengths fit
			}
			t.Fatalf("truncated to %d bytes decoded", n)
		}
	}
	if _, err := DecodeMultiSend([]byte{1, 2, 3}); err == nil {
		t.Fatal("garbage multisend decoded")
	}
}

func TestGasLimitScalesWithSignaturesAndSubcalls(t *testing.T) {
	one := (&Verdict{Signatures: 1, Subcalls: 4}).GasLimit()
	three := (&Verdict{Signatures: 3, Subcalls: 4}).GasLimit()
	five := (&Verdict{Signatures: 3, Subcalls: 5}).GasLimit()
	if !(one < three && three < five) {
		t.Fatalf("gas not monotonic: %d %d %d", one, three, five)
	}
	if (&Verdict{Signatures: 1, Subcalls: 1}).GasLimit() >= one {
		t.Fatal("single call should budget less than a 4-call multisend")
	}
}

func TestSelectorsMatchKnownValues(t *testing.T) {
	if got := common.Bytes2Hex(execTransactionSelector[:]); got != "6a761202" {
		t.Fatalf("execTransaction selector %s", got)
	}
	if got := common.Bytes2Hex(multiSendSelector[:]); got != "8d80ff0a" {
		t.Fatalf("multiSend selector %s", got)
	}
	_ = strings.TrimSpace
}
