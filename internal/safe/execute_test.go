package safe

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
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
		state *State
		nonce uint64
		err   error
	}

	owner struct {
		key  *ecdsa.PrivateKey
		addr common.Address
	}
)

func (f fakeSource) Transaction(context.Context, string) (*Transaction, error) { return f.tx, f.err }

func (f fakeExecState) ReadExecState(context.Context, common.Address) (*State, uint64, error) {
	return f.state, f.nonce, f.err
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

	packed, err := PackSignatures(hash, confirmations, addresses(owners), 2)
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

	if _, err := PackSignatures(hash, confirmations, addresses(owners), 3); !errors.Is(err, ErrNotReady) {
		t.Fatalf("three of three = %v, want ErrNotReady", err)
	}
	if _, err := PackSignatures(hash, confirmations, addresses(owners), 0); !errors.Is(err, ErrNotReady) {
		t.Fatalf("unknown threshold = %v, want ErrNotReady", err)
	}
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
