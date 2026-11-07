package safe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const (
	stsTx = `{"safe":"0x1111111111111111111111111111111111111111","to":"0x2222222222222222222222222222222222222222",
"value":"0","data":null,"operation":0,"gasToken":"0x0000000000000000000000000000000000000000","safeTxGas":0,"baseGas":0,
"gasPrice":"0","refundReceiver":null,"nonce":5,"isExecuted":false,"isSuccessful":null,"transactionHash":null,
"safeTxHash":"0xabc","confirmationsRequired":2,
"confirmations":[{"owner":"0xA1","signature":"0x11","submissionDate":"2026-10-25T10:00:00Z"}]}`
)

func TestSTSTransactionNormalizesUpstreamShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/multisig-transactions/0xabc/" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, stsTx)
	}))
	defer srv.Close()

	tx, err := NewSTS(srv.URL, "").Transaction(context.Background(), "0xabc")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Data != "0x" || tx.Nonce != "5" || tx.SafeTxGas != "0" || tx.RefundReceiver != "0x0000000000000000000000000000000000000000" ||
		tx.IsSuccessful != nil || tx.TransactionHash != nil || tx.ConfirmationsRequired != 2 ||
		len(tx.Confirmations) != 1 || tx.Confirmations[0].SubmittedAt != "2026-10-25T10:00:00Z" {
		t.Fatalf("unexpected %+v", tx)
	}
}

func TestSTSSendsKeyOnlyAsBearerAndNeverInErrors(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"nonce":["already used"]}`)
	}))
	defer srv.Close()

	err := NewSTS(srv.URL, "s3cret").Confirm(context.Background(), "0xabc", "0x11")
	var sErr *STSError
	if !errors.As(err, &sErr) || sErr.Status != 422 || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("got %v", err)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("auth header %q", auth)
	}
}

func TestSTSProposeBodyAndNonce(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/safes/0xsafe/multisig-transactions/" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	f := TxFields{To: "0x2", Value: "0", Data: "0x", Operation: 1, SafeTxGas: "0", BaseGas: "0", GasPrice: "0", GasToken: "0x0", RefundReceiver: "0x0", Nonce: "9"}
	if err := NewSTS(srv.URL, "").Propose(context.Background(), "0xsafe", "0xhash", "0xsender", "0xsig", f); err != nil {
		t.Fatal(err)
	}
	if body["contractTransactionHash"] != "0xhash" || body["sender"] != "0xsender" || body["nonce"] != float64(9) ||
		body["data"] != nil || body["operation"] != float64(1) {
		t.Fatalf("body %v", body)
	}
}

// The Transaction Service rejects an address that is not EIP-55, and the
// field it names is the one in the JSON body. The SafeTx hash is over the
// 20 bytes, so the checksum must change only the strings it is shown.
func TestSTSProposeChecksumsAddresses(t *testing.T) {
	const (
		safe   = "0x8fc371097031b44aaa0510e21f492072376e7b4f"
		token  = "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"
		refund = "0x1234567890abcdef1234567890abcdef12345678"
	)
	wantSafe := common.HexToAddress(safe).Hex()
	wantToken := common.HexToAddress(token).Hex()
	wantRefund := common.HexToAddress(refund).Hex()
	if wantSafe == safe || wantToken == token || wantRefund == refund {
		t.Fatal("fixture is already checksummed")
	}
	var (
		body map[string]any
		path string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	f := TxFields{
		To: safe, Value: "0", Data: "0xabcdef", Operation: 0,
		SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
		GasToken: token, RefundReceiver: refund, Nonce: "46",
	}
	if err := NewSTS(srv.URL, "").Propose(context.Background(), safe, "0xhash", "0xsender", "0xsig", f); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/safes/"+wantSafe+"/multisig-transactions/" {
		t.Fatalf("path %s", path)
	}
	if body["to"] != wantSafe || body["gasToken"] != wantToken || body["refundReceiver"] != wantRefund ||
		body["sender"] != "0xsender" || body["contractTransactionHash"] != "0xhash" {
		t.Fatalf("body %v", body)
	}
}

func TestSTSQueuedAndNonce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/safes/0xsafe/":
			_, _ = io.WriteString(w, `{"nonce":4,"threshold":2}`)
		default:
			if r.URL.Query().Get("nonce__gte") != "4" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"count":1,"results":[`+stsTx+`]}`)
		}
	}))
	defer srv.Close()
	c := NewSTS(srv.URL, "")
	if n, err := c.Nonce(context.Background(), "0xsafe"); err != nil || n != "4" {
		t.Fatalf("nonce %q %v", n, err)
	}
	list, err := c.Queued(context.Background(), "0xsafe", "4")
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
}

func TestSTSBoundsResponseAndSurfacesOutage(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", maxSTSBody+10))
	}))
	defer big.Close()
	if _, err := NewSTS(big.URL, "").Transaction(context.Background(), "0xabc"); err == nil {
		t.Fatal("oversized body accepted")
	}
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close()
	if _, err := NewSTS(down.URL, "k").Transaction(context.Background(), "0xabc"); err == nil || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("got %v", err)
	}
}
