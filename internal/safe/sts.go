package safe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// maxSTSBody bounds what is read from the Transaction Service, so a
	// misbehaving upstream cannot exhaust memory.
	maxSTSBody   = 4 << 20
	stsTimeout   = 10 * time.Second
	apiKeyHeader = "Authorization"
)

// STSError is a non-2xx answer from the Transaction Service. Status is the
// upstream status; Message is a bounded excerpt, never a credential.
type STSError struct {
	Status  int
	Message string
}

func (e *STSError) Error() string {
	return fmt.Sprintf("safe transaction service: status %d: %s", e.Status, e.Message)
}

// STS is a client for the Safe Transaction Service. The API key stays in this
// struct and is only ever written to the outgoing request header.
type STS struct {
	base   string
	apiKey string
	http   *http.Client
}

func NewSTS(baseURL, apiKey string) *STS {
	return &STS{
		base:   strings.TrimRight(baseURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: stsTimeout},
	}
}

// stsTransaction is the upstream shape. nonce and the gas fields arrive as
// numbers or strings depending on the field, so they are read as json.Number.
type stsTransaction struct {
	Safe                  string      `json:"safe"`
	To                    string      `json:"to"`
	Value                 json.Number `json:"value"`
	Data                  *string     `json:"data"`
	Operation             uint8       `json:"operation"`
	GasToken              string      `json:"gasToken"`
	SafeTxGas             json.Number `json:"safeTxGas"`
	BaseGas               json.Number `json:"baseGas"`
	GasPrice              json.Number `json:"gasPrice"`
	RefundReceiver        *string     `json:"refundReceiver"`
	Nonce                 json.Number `json:"nonce"`
	IsExecuted            bool        `json:"isExecuted"`
	IsSuccessful          *bool       `json:"isSuccessful"`
	TransactionHash       *string     `json:"transactionHash"`
	SafeTxHash            string      `json:"safeTxHash"`
	ConfirmationsRequired int         `json:"confirmationsRequired"`
	Confirmations         []struct {
		Owner          string `json:"owner"`
		Signature      string `json:"signature"`
		SubmissionDate string `json:"submissionDate"`
	} `json:"confirmations"`
}

func (t stsTransaction) transaction() Transaction {
	data := "0x"
	if t.Data != nil && *t.Data != "" {
		data = *t.Data
	}
	refund := "0x0000000000000000000000000000000000000000"
	if t.RefundReceiver != nil && *t.RefundReceiver != "" {
		refund = *t.RefundReceiver
	}
	out := Transaction{
		Safe:       t.Safe,
		SafeTxHash: t.SafeTxHash,
		TxFields: TxFields{
			To: t.To, Value: t.Value.String(), Data: data, Operation: t.Operation,
			SafeTxGas: t.SafeTxGas.String(), BaseGas: t.BaseGas.String(), GasPrice: t.GasPrice.String(),
			GasToken: t.GasToken, RefundReceiver: refund, Nonce: t.Nonce.String(),
		},
		Confirmations:         make([]Confirmation, 0, len(t.Confirmations)),
		ConfirmationsRequired: t.ConfirmationsRequired,
		IsExecuted:            t.IsExecuted,
		IsSuccessful:          t.IsSuccessful,
		TransactionHash:       t.TransactionHash,
	}
	for _, c := range t.Confirmations {
		out.Confirmations = append(out.Confirmations, Confirmation{Owner: c.Owner, Signature: c.Signature, SubmittedAt: c.SubmissionDate})
	}
	return out
}

func (s *STS) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set(apiKeyHeader, "Bearer "+s.apiKey)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		// url.Error carries the URL but never request headers.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("safe transaction service unreachable: %w", uerr.Err)
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSTSBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxSTSBody {
		return errors.New("safe transaction service: response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &STSError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Propose registers a transaction and its first signature.
func (s *STS) Propose(ctx context.Context, safe, safeTxHash, sender, signature string, f TxFields) error {
	var data *string
	if f.Data != "" && f.Data != "0x" {
		data = &f.Data
	}
	body := map[string]any{
		"to": f.To, "value": json.Number(f.Value), "data": data, "operation": f.Operation,
		"gasToken": f.GasToken, "safeTxGas": json.Number(f.SafeTxGas), "baseGas": json.Number(f.BaseGas),
		"gasPrice": json.Number(f.GasPrice), "refundReceiver": f.RefundReceiver, "nonce": json.Number(f.Nonce),
		"contractTransactionHash": safeTxHash, "sender": sender, "signature": signature,
	}
	return s.do(ctx, http.MethodPost, "/api/v1/safes/"+safe+"/multisig-transactions/", body, nil)
}

// Confirm adds a signature to a proposed transaction.
func (s *STS) Confirm(ctx context.Context, safeTxHash, signature string) error {
	return s.do(ctx, http.MethodPost, "/api/v1/multisig-transactions/"+safeTxHash+"/confirmations/",
		map[string]string{"signature": signature}, nil)
}

func (s *STS) Transaction(ctx context.Context, safeTxHash string) (*Transaction, error) {
	var raw stsTransaction
	if err := s.do(ctx, http.MethodGet, "/api/v1/multisig-transactions/"+safeTxHash+"/", nil, &raw); err != nil {
		return nil, err
	}
	tx := raw.transaction()
	return &tx, nil
}

// Nonce is the Safe's current nonce: the first one not yet executed.
func (s *STS) Nonce(ctx context.Context, safe string) (string, error) {
	var info struct {
		Nonce json.Number `json:"nonce"`
	}
	if err := s.do(ctx, http.MethodGet, "/api/v1/safes/"+safe+"/", nil, &info); err != nil {
		return "", err
	}
	return info.Nonce.String(), nil
}

// Queued lists transactions at or above nonce, oldest first.
func (s *STS) Queued(ctx context.Context, safe, nonceGte string) ([]Transaction, error) {
	var page struct {
		Results []stsTransaction `json:"results"`
	}
	q := url.Values{"nonce__gte": {nonceGte}, "executed": {"false"}, "ordering": {"nonce"}}
	if err := s.do(ctx, http.MethodGet, "/api/v1/safes/"+safe+"/multisig-transactions/?"+q.Encode(), nil, &page); err != nil {
		return nil, err
	}
	out := make([]Transaction, 0, len(page.Results))
	for _, r := range page.Results {
		out = append(out, r.transaction())
	}
	return out, nil
}
