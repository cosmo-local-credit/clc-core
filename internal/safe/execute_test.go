package safe

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type (
	fakeSource struct {
		tx  *Transaction
		err error
	}

	fakeExecState struct {
		state     *State
		nonce     uint64
		err       error
		contracts ContractVerifier
	}

	// fakeContracts answers for contract signatures and records the owners
	// it was asked to verify.
	fakeContracts struct {
		accepts    bool
		acceptsErr error
		valid      map[common.Address]bool
		validErr   error
		asked      []common.Address
	}

	owner struct {
		key  *ecdsa.PrivateKey
		addr common.Address
	}
)

func (f fakeSource) Transaction(context.Context, string) (*Transaction, error) { return f.tx, f.err }

func (f fakeExecState) ReadExecState(context.Context, common.Address) (*ExecState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ExecState{State: *f.state, Nonce: f.nonce, Contracts: f.contracts}, nil
}

func (f *fakeContracts) Accepts(context.Context) (bool, error) { return f.accepts, f.acceptsErr }

func (f *fakeContracts) Valid(_ context.Context, owner common.Address, _ common.Hash, _ []byte) (bool, error) {
	f.asked = append(f.asked, owner)
	return f.valid[owner], f.validErr
}

// contractConfirmation is a contract owner's confirmation as the Transaction
// Service returns it.
func contractConfirmation(owner common.Address, data []byte) Confirmation {
	return Confirmation{Owner: owner.Hex(), Signature: hexutil.Encode(contractSig(owner, data))}
}

func newOwners(t *testing.T, n int) []owner {
	t.Helper()
	out := make([]owner, n)
	for i := range out {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		out[i] = owner{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
	}
	return out
}

// typedSig is what eth_signTypedData returns for the SafeTx digest.
func typedSig(t *testing.T, o owner, hash common.Hash) Confirmation {
	t.Helper()
	sig, err := crypto.Sign(hash[:], o.key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return Confirmation{Owner: o.addr.Hex(), Signature: hexutil.Encode(sig)}
}

// ethSignSig is eth_sign over the digest, which Safe marks with v + 4.
func ethSignSig(t *testing.T, o owner, hash common.Hash) Confirmation {
	t.Helper()
	sig, err := crypto.Sign(accounts.TextHash(hash[:]), o.key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 31
	return Confirmation{Owner: o.addr.Hex(), Signature: hexutil.Encode(sig)}
}

func addresses(owners []owner) []common.Address {
	out := make([]common.Address, len(owners))
	for i, o := range owners {
		out[i] = o.addr
	}
	return out
}

func TestPackSignaturesVerifiesSortsAndTrimsToThreshold(t *testing.T) {
	hash := common.HexToHash("0x" + "ab" + "00000000000000000000000000000000000000000000000000000000000000")
	owners := newOwners(t, 3)
	stranger := newOwners(t, 1)[0]
	confirmations := []Confirmation{
		typedSig(t, owners[0], hash),
		ethSignSig(t, owners[1], hash),
		typedSig(t, owners[0], hash),                          // duplicate
		typedSig(t, stranger, hash),                           // not an owner
		{Owner: owners[2].addr.Hex(), Signature: "0x" + "00"}, // malformed
		{Owner: owners[2].addr.Hex(), Signature: hexutil.Encode(append(make([]byte, 64), 0))}, // contract signature
	}
	// A valid signature filed under another owner's name is not that owner's.
	misfiled := typedSig(t, owners[2], hash)
	misfiled.Owner = owners[0].addr.Hex()
	confirmations = append(confirmations, misfiled)

	ctx := context.Background()
	packed, err := PackSignatures(ctx, hash, confirmations, addresses(owners), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) != 130 {
		t.Fatalf("packed %d bytes, want two signatures", len(packed))
	}
	first, err := recoverOwnerSignature(hash, packed[:65])
	if err != nil {
		t.Fatal(err)
	}
	second, err := recoverOwnerSignature(hash, packed[65:])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(first[:], second[:]) >= 0 {
		t.Fatalf("signatures not in ascending owner order: %s, %s", first.Hex(), second.Hex())
	}

	if _, err := PackSignatures(ctx, hash, confirmations, addresses(owners), 3, nil); !errors.Is(err, ErrNotReady) {
		t.Fatalf("three of three = %v, want ErrNotReady", err)
	}
	if _, err := PackSignatures(ctx, hash, confirmations, addresses(owners), 0, nil); !errors.Is(err, ErrNotReady) {
		t.Fatalf("unknown threshold = %v, want ErrNotReady", err)
	}
}

func TestPackSignaturesKeepsTheECDSAOnlyLayout(t *testing.T) {
	hash := common.HexToHash("0x" + "ef" + "00000000000000000000000000000000000000000000000000000000000000")
	owners := newOwners(t, 3)
	confirmations := []Confirmation{typedSig(t, owners[0], hash), typedSig(t, owners[1], hash), typedSig(t, owners[2], hash)}
	contracts := &fakeContracts{accepts: true}
	packed, err := PackSignatures(context.Background(), hash, confirmations, addresses(owners), 2, contracts)
	if err != nil {
		t.Fatal(err)
	}
	// The two lowest owners' signatures, concatenated: what clc-core packed
	// before contract signatures existed.
	byOwner := map[common.Address]string{}
	for _, c := range confirmations {
		byOwner[common.HexToAddress(c.Owner)] = c.Signature
	}
	signers := addresses(owners)
	slices.SortFunc(signers, func(a, b common.Address) int { return bytes.Compare(a[:], b[:]) })
	want := append(hexutil.MustDecode(byOwner[signers[0]]), hexutil.MustDecode(byOwner[signers[1]])...)
	if !bytes.Equal(packed, want) {
		t.Fatalf("packed %x, want %x", packed, want)
	}
	if len(contracts.asked) != 0 {
		t.Fatal("ECDSA signatures met the threshold, yet a contract signature was verified")
	}
}

func TestPackSignaturesFillsWithContractSignatures(t *testing.T) {
	hash := common.HexToHash("0x" + "12" + "00000000000000000000000000000000000000000000000000000000000000")
	ecdsaOwners := newOwners(t, 2)
	passkeyA := common.HexToAddress("0x0000000000000000000000000000000000000001")
	passkeyB := common.HexToAddress("0xffffffffffffffffffffffffffffffffffffff01")
	passkeyC := common.HexToAddress("0x8000000000000000000000000000000000000001")
	owners := append(addresses(ecdsaOwners), passkeyA, passkeyB, passkeyC)
	dataA, dataB, dataC := bytes.Repeat([]byte{0xaa}, 33), bytes.Repeat([]byte{0xbb}, 70), bytes.Repeat([]byte{0xcc}, 5)
	ctx := context.Background()
	stranger := common.HexToAddress("0x0000000000000000000000000000000000000002")

	confirmations := []Confirmation{
		typedSig(t, ecdsaOwners[0], hash),
		contractConfirmation(passkeyB, dataB),
		contractConfirmation(passkeyA, dataA),
		contractConfirmation(stranger, dataA),                                            // not an owner
		{Owner: passkeyC.Hex(), Signature: hexutil.Encode(contractSig(passkeyA, dataC))}, // filed under another owner
	}

	t.Run("one ECDSA and two contract signatures", func(t *testing.T) {
		contracts := &fakeContracts{accepts: true, valid: map[common.Address]bool{passkeyA: true, passkeyB: true}}
		packed, err := PackSignatures(ctx, hash, confirmations, owners, 3, contracts)
		if err != nil {
			t.Fatal(err)
		}
		// Ascending: passkeyA, then the ECDSA owner (between 0x00.. and
		// 0xff.. for any key), then passkeyB.
		static := packed[:3*65]
		if got := common.BytesToAddress(static[12:32]); got != passkeyA || static[64] != 0 {
			t.Fatalf("first entry %s v%d", got.Hex(), static[64])
		}
		signer, err := recoverOwnerSignature(hash, static[65:130])
		if err != nil || signer != ecdsaOwners[0].addr {
			t.Fatalf("second entry %s %v", signer.Hex(), err)
		}
		if got := common.BytesToAddress(static[130+12 : 130+32]); got != passkeyB || static[194] != 0 {
			t.Fatalf("third entry %s v%d", got.Hex(), static[194])
		}
		offsetA := new(big.Int).SetBytes(static[32:64]).Int64()
		offsetB := new(big.Int).SetBytes(static[130+32 : 130+64]).Int64()
		if offsetA != 195 || offsetB != 195+32+33 {
			t.Fatalf("offsets %d, %d", offsetA, offsetB)
		}
		readDynamic := func(offset int64) []byte {
			length := new(big.Int).SetBytes(packed[offset : offset+32]).Int64()
			return packed[offset+32 : offset+32+length]
		}
		if !bytes.Equal(readDynamic(offsetA), dataA) || !bytes.Equal(readDynamic(offsetB), dataB) {
			t.Fatal("dynamic parts do not match their owners")
		}
		if len(packed) != 195+32+33+32+70 {
			t.Fatalf("packed %d bytes", len(packed))
		}
	})

	t.Run("verifies only as many as the threshold needs", func(t *testing.T) {
		contracts := &fakeContracts{accepts: true, valid: map[common.Address]bool{passkeyA: true, passkeyB: true}}
		if _, err := PackSignatures(ctx, hash, confirmations, owners, 2, contracts); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(contracts.asked, []common.Address{passkeyA}) {
			t.Fatalf("verified %v", contracts.asked)
		}
	})

	t.Run("skips an invalid contract signature", func(t *testing.T) {
		contracts := &fakeContracts{accepts: true, valid: map[common.Address]bool{passkeyB: true}}
		if _, err := PackSignatures(ctx, hash, confirmations, owners, 3, contracts); !errors.Is(err, ErrNotReady) {
			t.Fatalf("got %v, want ErrNotReady", err)
		}
	})

	t.Run("a Safe before 1.5.0 counts none", func(t *testing.T) {
		contracts := &fakeContracts{valid: map[common.Address]bool{passkeyA: true, passkeyB: true}}
		if _, err := PackSignatures(ctx, hash, confirmations, owners, 2, contracts); !errors.Is(err, ErrNotReady) {
			t.Fatalf("got %v, want ErrNotReady", err)
		}
		if len(contracts.asked) != 0 {
			t.Fatal("verified a contract signature the Safe cannot accept")
		}
	})

	t.Run("no verifier counts none", func(t *testing.T) {
		if _, err := PackSignatures(ctx, hash, confirmations, owners, 2, nil); !errors.Is(err, ErrNotReady) {
			t.Fatalf("got %v, want ErrNotReady", err)
		}
	})

	t.Run("transport failures are returned for retry", func(t *testing.T) {
		boom := errors.New("dial tcp")
		for name, contracts := range map[string]*fakeContracts{
			"version": {acceptsErr: boom},
			"verify":  {accepts: true, validErr: boom},
		} {
			if _, err := PackSignatures(ctx, hash, confirmations, owners, 2, contracts); !errors.Is(err, boom) || errors.Is(err, ErrNotReady) {
				t.Fatalf("%s: got %v, want the transport error", name, err)
			}
		}
	})
}

func TestEncodeExecTransactionRoundTrips(t *testing.T) {
	exec, err := fieldsFor(assetAddr, "4").Exec()
	if err != nil {
		t.Fatal(err)
	}
	exec.Signatures = bytes.Repeat([]byte{0x11}, 130)
	data, err := EncodeExecTransaction(exec)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeExec(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.To != exec.To || !bytes.Equal(back.Data, exec.Data) || !bytes.Equal(back.Signatures, exec.Signatures) {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}

func fieldsFor(to common.Address, nonce string) TxFields {
	return TxFields{
		To: to.Hex(), Value: "0", Data: hexutil.Encode(transferOwnership(holder)), Operation: 0,
		SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: common.Address{}.Hex(), RefundReceiver: common.Address{}.Hex(), Nonce: nonce,
	}
}

func TestExecutorPrepare(t *testing.T) {
	owners := newOwners(t, 3)
	stored := func(t *testing.T, fields TxFields, signers ...owner) (*Transaction, common.Hash) {
		t.Helper()
		hash, err := SafeTxHash(100, safeAddr, fields)
		if err != nil {
			t.Fatal(err)
		}
		tx := &Transaction{TxFields: fields, Safe: safeAddr.Hex(), SafeTxHash: hash.Hex(), ConfirmationsRequired: 2}
		for _, s := range signers {
			tx.Confirmations = append(tx.Confirmations, typedSig(t, s, hash))
		}
		return tx, hash
	}
	chain := fakeExecState{state: &State{Owners: addresses(owners), Threshold: 2}, nonce: 4}
	prepare := func(tx *Transaction, hash common.Hash, reader fakeExecState) (*Execution, error) {
		return NewExecutor(100, newPolicy(), fakeSource{tx: tx}, reader).Prepare(context.Background(), hash.Hex())
	}

	t.Run("builds the call with threshold signatures", func(t *testing.T) {
		tx, hash := stored(t, fieldsFor(assetAddr, "4"), owners[0], owners[2])
		got, err := prepare(tx, hash, chain)
		if err != nil {
			t.Fatal(err)
		}
		exec, err := DecodeExec(got.Calldata)
		if err != nil {
			t.Fatal(err)
		}
		if got.Safe != safeAddr || exec.To != assetAddr || len(exec.Signatures) != 130 {
			t.Fatalf("unexpected execution %+v", exec)
		}
	})

	for name, tc := range map[string]struct {
		mutate func(*Transaction)
		reader fakeExecState
		fields TxFields
		want   error
	}{
		"already executed":       {mutate: func(tx *Transaction) { tx.IsExecuted = true }, reader: chain, want: ErrSettled},
		"nonce used by another":  {reader: fakeExecState{state: chain.state, nonce: 5}, want: ErrSettled},
		"earlier nonce pending":  {reader: fakeExecState{state: chain.state, nonce: 3}, want: ErrNotNext},
		"one signature of two":   {mutate: func(tx *Transaction) { tx.Confirmations = tx.Confirmations[:1] }, reader: chain, want: ErrNotReady},
		"tampered field":         {mutate: func(tx *Transaction) { tx.Value = "1" }, reader: chain, want: ErrRejected},
		"asset of another owner": {fields: fieldsFor(strangerAddr, "4"), reader: chain, want: ErrRejected},
		"owner no longer a Safe": {reader: fakeExecState{err: ErrNotSafe}, want: ErrRejected},
	} {
		t.Run(name, func(t *testing.T) {
			fields := tc.fields
			if fields.To == "" {
				fields = fieldsFor(assetAddr, "4")
			}
			tx, hash := stored(t, fields, owners[0], owners[1])
			if tc.mutate != nil {
				tc.mutate(tx)
			}
			if _, err := prepare(tx, hash, tc.reader); !errors.Is(err, tc.want) {
				t.Fatalf("Prepare() = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("transport failures are returned for retry", func(t *testing.T) {
		tx, hash := stored(t, fieldsFor(assetAddr, "4"), owners[0], owners[1])
		boom := errors.New("rpc down")
		if _, err := prepare(tx, hash, fakeExecState{err: boom}); !errors.Is(err, boom) {
			t.Fatalf("Prepare() = %v, want the read error", err)
		}
	})
}
