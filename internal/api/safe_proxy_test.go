package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v5"
)

var (
	proxySafe  = common.HexToAddress("0x1111111111111111111111111111111111111111")
	proxyAsset = common.HexToAddress("0x2222222222222222222222222222222222222222")
	proxyOther = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

type (
	assetResolver struct{}

	fakeSigners struct {
		signers []string
		ok      bool
		err     error
	}

	fakeSTS struct {
		stored    *safe.Transaction
		proposed  []string
		confirmed []string
		queued    []safe.Transaction
		err       error
	}

	proxyFixture struct {
		svc  *safeService
		sts  *fakeSTS
		key  string // signer address
		sign func(common.Hash) string
	}
)

func (assetResolver) IsSafe(context.Context, common.Address) (bool, error) { return false, nil }
func (assetResolver) ManagesAsset(_ context.Context, _, target common.Address) (bool, error) {
	return target == proxyAsset, nil
}

func (f fakeSigners) Signers(context.Context, common.Address) ([]string, bool, error) {
	return f.signers, f.ok, f.err
}

func (f *fakeSTS) Propose(_ context.Context, _, hash, sender, _ string, _ safe.TxFields) error {
	if f.err != nil {
		return f.err
	}
	f.proposed = append(f.proposed, hash+":"+sender)
	return nil
}

func (f *fakeSTS) Confirm(_ context.Context, hash, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.confirmed = append(f.confirmed, hash)
	return nil
}

func (f *fakeSTS) Transaction(context.Context, string) (*safe.Transaction, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.stored, nil
}

func (f *fakeSTS) Nonce(context.Context, string) (string, error) { return "4", f.err }

func (f *fakeSTS) Queued(context.Context, string, string) ([]safe.Transaction, error) {
	return f.queued, f.err
}

func ownershipCall() []byte {
	return append([]byte{0xf2, 0xfd, 0xe3, 0x8b}, common.LeftPadBytes(proxyOther.Bytes(), 32)...)
}

func txFor(to common.Address, data []byte) safe.TxFields {
	return safe.TxFields{
		To: to.Hex(), Value: "0", Data: hexutil.Encode(data), Operation: 0,
		SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: common.Address{}.Hex(), RefundReceiver: common.Address{}.Hex(), Nonce: "4",
	}
}

func newProxyFixture(t *testing.T, signersOK bool) *proxyFixture {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	sts := &fakeSTS{}
	return &proxyFixture{
		sts: sts,
		key: addr,
		svc: &safeService{
			chainID: 100,
			policy:  safe.NewPolicy(assetResolver{}, nil, safe.DefaultAllowedSignatures),
			sts:     sts,
			signers: fakeSigners{signers: []string{strings.ToLower(addr), proxyOther.Hex()}, ok: signersOK},
		},
		sign: func(h common.Hash) string {
			sig, _ := crypto.Sign(h[:], key)
			sig[64] += 27
			return hexutil.Encode(sig)
		},
	}
}

func (f *proxyFixture) proposal(t *testing.T, fields safe.TxFields) safeProposalRequest {
	t.Helper()
	hash, err := safe.SafeTxHash(100, proxySafe, fields)
	if err != nil {
		t.Fatal(err)
	}
	return safeProposalRequest{Safe: proxySafe.Hex(), SafeTxHash: hash.Hex(), Sender: f.key, Signature: f.sign(hash), Tx: fields}
}

func status(t *testing.T, err error) int {
	t.Helper()
	var pErr *proxyError
	if !errors.As(err, &pErr) {
		t.Fatalf("want proxyError, got %v", err)
	}
	return pErr.status
}

func TestProposeAcceptsASignersPolicyCompliantTransaction(t *testing.T) {
	f := newProxyFixture(t, true)
	req := f.proposal(t, txFor(proxyAsset, ownershipCall()))
	f.sts.stored = &safe.Transaction{Safe: proxySafe.Hex(), SafeTxHash: req.SafeTxHash}
	tx, err := f.svc.propose(context.Background(), f.key, req)
	if err != nil || tx.SafeTxHash != req.SafeTxHash {
		t.Fatalf("got %v, %v", tx, err)
	}
	if len(f.sts.proposed) != 1 || !strings.EqualFold(f.sts.proposed[0], req.SafeTxHash+":"+f.key) {
		t.Fatalf("proposed %v", f.sts.proposed)
	}
}

func TestProposeRejections(t *testing.T) {
	good := txFor(proxyAsset, ownershipCall())
	cases := map[string]struct {
		want   int
		mutate func(f *proxyFixture, r *safeProposalRequest, caller *string)
		stale  bool
		err    error
	}{
		"wrong sender": {403, func(f *proxyFixture, r *safeProposalRequest, c *string) { *c = proxyOther.Hex() }, false, nil},
		"not a signer": {403, func(f *proxyFixture, r *safeProposalRequest, c *string) {
			f.svc.signers = fakeSigners{signers: []string{proxyOther.Hex()}, ok: true}
		}, false, nil},
		"stale or unknown": {403, func(f *proxyFixture, r *safeProposalRequest, c *string) {
			f.svc.signers = fakeSigners{signers: []string{f.key}, ok: false}
		}, false, nil},
		"signer lookup": {503, func(f *proxyFixture, r *safeProposalRequest, c *string) {
			f.svc.signers = fakeSigners{err: errors.New("db")}
		}, false, nil},
		"hash mismatch": {400, func(f *proxyFixture, r *safeProposalRequest, c *string) { r.Tx.Nonce = "5" }, false, nil},
		"foreign signature": {400, func(f *proxyFixture, r *safeProposalRequest, c *string) {
			other, _ := crypto.GenerateKey()
			h := common.HexToHash(r.SafeTxHash)
			sig, _ := crypto.Sign(h[:], other)
			sig[64] += 27
			r.Signature = hexutil.Encode(sig)
		}, false, nil},
		"bad hash format":   {400, func(f *proxyFixture, r *safeProposalRequest, c *string) { r.SafeTxHash = "0x12" }, false, nil},
		"upstream conflict": {409, func(f *proxyFixture, r *safeProposalRequest, c *string) {}, false, &safe.STSError{Status: 422, Message: "nonce used"}},
		"upstream down":     {502, func(f *proxyFixture, r *safeProposalRequest, c *string) {}, false, errors.New("dial tcp")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newProxyFixture(t, true)
			req := f.proposal(t, good)
			caller := f.key
			tc.mutate(f, &req, &caller)
			f.sts.err = tc.err
			_, err := f.svc.propose(context.Background(), caller, req)
			if got := status(t, err); got != tc.want {
				t.Fatalf("status %d, want %d (%v)", got, tc.want, err)
			}
			if tc.err == nil && len(f.sts.proposed) != 0 {
				t.Fatal("rejected proposal reached the Transaction Service")
			}
		})
	}
}

func TestProposeRejectsHostileTransactions(t *testing.T) {
	hostile := map[string]safe.TxFields{
		"asset the Safe does not own": txFor(proxyOther, ownershipCall()),
		"selector off the allowlist":  txFor(proxyAsset, []byte{0xde, 0xad, 0xbe, 0xef}),
		"self call":                   txFor(proxySafe, []byte{0xe3, 0x18, 0xb5, 0x2b, 0, 0, 0, 0}),
	}
	gas := txFor(proxyAsset, ownershipCall())
	gas.GasPrice = "1"
	hostile["gas refund"] = gas
	delegate := txFor(proxyAsset, ownershipCall())
	delegate.Operation = 1
	hostile["delegatecall to a non-MultiSend"] = delegate

	for name, fields := range hostile {
		t.Run(name, func(t *testing.T) {
			f := newProxyFixture(t, true)
			_, err := f.svc.propose(context.Background(), f.key, f.proposal(t, fields))
			if got := status(t, err); got != http.StatusForbidden {
				t.Fatalf("status %d, want 403 (%v)", got, err)
			}
			if len(f.sts.proposed) != 0 {
				t.Fatal("hostile proposal reached the Transaction Service")
			}
		})
	}
}

func storedFor(t *testing.T, fields safe.TxFields) *safe.Transaction {
	t.Helper()
	hash, err := safe.SafeTxHash(100, proxySafe, fields)
	if err != nil {
		t.Fatal(err)
	}
	return &safe.Transaction{Safe: proxySafe.Hex(), SafeTxHash: hash.Hex(), TxFields: fields}
}

func TestConfirmRequiresTheCallersOwnSignatureAndAPolicyCompliantTransaction(t *testing.T) {
	f := newProxyFixture(t, true)
	f.sts.stored = storedFor(t, txFor(proxyAsset, ownershipCall()))
	hash := common.HexToHash(f.sts.stored.SafeTxHash)

	if _, err := f.svc.confirm(context.Background(), f.key, safeConfirmationRequest{SafeTxHash: hash.Hex(), Signature: f.sign(hash)}); err != nil {
		t.Fatal(err)
	}
	if len(f.sts.confirmed) != 1 {
		t.Fatal("confirmation not forwarded")
	}

	// A signature from someone else, even a valid one, is not the caller's.
	otherKey, _ := crypto.GenerateKey()
	sig, _ := crypto.Sign(hash[:], otherKey)
	sig[64] += 27
	_, err := f.svc.confirm(context.Background(), f.key, safeConfirmationRequest{SafeTxHash: hash.Hex(), Signature: hexutil.Encode(sig)})
	if got := status(t, err); got != 403 {
		t.Fatalf("foreign signature status %d", got)
	}

	// A non-signer may not confirm.
	f.svc.signers = fakeSigners{signers: []string{proxyOther.Hex()}, ok: true}
	_, err = f.svc.confirm(context.Background(), f.key, safeConfirmationRequest{SafeTxHash: hash.Hex(), Signature: f.sign(hash)})
	if got := status(t, err); got != 403 {
		t.Fatalf("non-signer status %d", got)
	}

	// A transaction queued outside this proxy that policy would refuse gains nothing.
	f2 := newProxyFixture(t, true)
	f2.sts.stored = storedFor(t, txFor(proxyOther, ownershipCall()))
	h2 := common.HexToHash(f2.sts.stored.SafeTxHash)
	_, err = f2.svc.confirm(context.Background(), f2.key, safeConfirmationRequest{SafeTxHash: h2.Hex(), Signature: f2.sign(h2)})
	if got := status(t, err); got != 403 || len(f2.sts.confirmed) != 0 {
		t.Fatalf("hostile stored tx status %d confirmed %v", got, f2.sts.confirmed)
	}

	// Unknown hash.
	f3 := newProxyFixture(t, true)
	f3.sts.err = &safe.STSError{Status: 404}
	_, err = f3.svc.confirm(context.Background(), f3.key, safeConfirmationRequest{SafeTxHash: hash.Hex(), Signature: f3.sign(hash)})
	if got := status(t, err); got != 404 {
		t.Fatalf("unknown status %d", got)
	}
}

func TestPendingAndTransactionRequireASigner(t *testing.T) {
	f := newProxyFixture(t, true)
	mine := safe.Transaction{Safe: proxySafe.Hex()}
	executed := safe.Transaction{Safe: proxySafe.Hex(), IsExecuted: true}
	foreign := safe.Transaction{Safe: proxyOther.Hex()}
	f.sts.queued = []safe.Transaction{mine, executed, foreign}
	f.sts.stored = storedFor(t, txFor(proxyAsset, ownershipCall()))

	results, err := f.svc.pending(context.Background(), f.key, strings.ToLower(proxySafe.Hex()))
	if err != nil || len(results) != 1 {
		t.Fatalf("pending %v, %v", results, err)
	}
	if _, err := f.svc.transaction(context.Background(), f.key, f.sts.stored.SafeTxHash); err != nil {
		t.Fatal(err)
	}

	f.svc.signers = fakeSigners{signers: []string{proxyOther.Hex()}, ok: true}
	_, err = f.svc.pending(context.Background(), f.key, proxySafe.Hex())
	if got := status(t, err); got != 403 {
		t.Fatalf("pending non-signer %d", got)
	}
	_, err = f.svc.transaction(context.Background(), f.key, f.sts.stored.SafeTxHash)
	if got := status(t, err); got != 403 {
		t.Fatalf("transaction non-signer %d", got)
	}
	_, err = f.svc.pending(context.Background(), f.key, "nope")
	if got := status(t, err); got != 400 {
		t.Fatalf("bad address %d", got)
	}
}

func TestProxyResultIsFlattenedIntoTheEnvelope(t *testing.T) {
	a := &API{}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	paid := true
	tx := safe.Transaction{Safe: proxySafe.Hex(), SafeTxHash: "0xabc", IsSuccessful: &paid,
		Confirmations: []safe.Confirmation{{Owner: "0x1", Signature: "0x2", SubmittedAt: "t"}}}
	if err := a.writeProxyResult(c, "Safe transaction", tx); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Ok     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"safe", "safeTxHash", "to", "value", "data", "operation", "safeTxGas", "baseGas", "gasPrice",
		"gasToken", "refundReceiver", "nonce", "confirmations", "confirmationsRequired", "isExecuted", "isSuccessful", "transactionHash"} {
		if _, ok := body.Result[key]; !ok {
			t.Errorf("result lacks %q", key)
		}
	}
	if !body.Ok || body.Result["transactionHash"] != nil || body.Result["isSuccessful"] != true {
		t.Fatalf("unexpected %v", body)
	}
}

// The confirmation that reaches the threshold hands execution to clc-core;
// one below it does not, and the caller's confirmation stands either way.
func TestConfirmationReachingTheThresholdQueuesExecution(t *testing.T) {
	for name, tc := range map[string]struct {
		confirmations int
		queued        bool
	}{
		"threshold reached": {confirmations: 2, queued: true},
		"one short":         {confirmations: 1, queued: false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newProxyFixture(t, true)
			f.sts.stored = storedFor(t, txFor(proxyAsset, ownershipCall()))
			f.sts.stored.ConfirmationsRequired = 2
			f.sts.stored.Confirmations = make([]safe.Confirmation, tc.confirmations)
			var calls []string
			f.svc.execute = func(_ context.Context, safeAddress, safeTxHash string) bool {
				calls = append(calls, safeAddress+":"+safeTxHash)
				return true
			}
			hash := common.HexToHash(f.sts.stored.SafeTxHash)
			tx, err := f.svc.confirm(context.Background(), f.key, safeConfirmationRequest{SafeTxHash: hash.Hex(), Signature: f.sign(hash)})
			if err != nil {
				t.Fatal(err)
			}
			if len(f.sts.confirmed) != 1 {
				t.Fatal("confirmation not forwarded")
			}
			if tc.queued != (len(calls) == 1) || tx.Executing != tc.queued {
				t.Fatalf("queued %v executing %v, want %v", calls, tx.Executing, tc.queued)
			}
		})
	}
}

// A threshold-one Safe executes straight from the proposal.
func TestProposalOfAThresholdOneSafeQueuesExecution(t *testing.T) {
	f := newProxyFixture(t, true)
	req := f.proposal(t, txFor(proxyAsset, ownershipCall()))
	f.sts.stored = &safe.Transaction{Safe: proxySafe.Hex(), SafeTxHash: req.SafeTxHash, TxFields: req.Tx,
		ConfirmationsRequired: 1, Confirmations: make([]safe.Confirmation, 1)}
	queued := 0
	f.svc.execute = func(context.Context, string, string) bool { queued++; return true }
	tx, err := f.svc.propose(context.Background(), f.key, req)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 1 || !tx.Executing {
		t.Fatalf("queued %d executing %v", queued, tx.Executing)
	}
}

func TestPendingReportsWhichTransactionsAreExecuting(t *testing.T) {
	f := newProxyFixture(t, true)
	a := storedFor(t, txFor(proxyAsset, ownershipCall()))
	b := storedFor(t, func() safe.TxFields { tx := txFor(proxyAsset, ownershipCall()); tx.Nonce = "5"; return tx }())
	f.sts.queued = []safe.Transaction{*a, *b}
	f.svc.executing = func(_ context.Context, hashes []string) map[string]bool {
		return map[string]bool{hashes[1]: true}
	}
	got, err := f.svc.pending(context.Background(), f.key, proxySafe.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Executing || !got[1].Executing {
		t.Fatalf("executing flags %v %v", got[0].Executing, got[1].Executing)
	}
}
