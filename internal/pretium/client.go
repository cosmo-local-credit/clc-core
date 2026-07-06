package pretium

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

const (
	contentType = "application/json"
)

type (
	PretiumClient struct {
		endpoint   string
		httpClient *http.Client
	}

	OKResponse struct {
		Ok          bool           `json:"ok"`
		Description string         `json:"description"`
		Result      map[string]any `json:"result"`
	}

	ErrResponse struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}

	APIError struct {
		StatusCode  int
		Description string
		Body        string
	}
)

func New(endpoint string) *PretiumClient {
	return &PretiumClient{
		endpoint: strings.TrimRight(endpoint, "/") + "/",
		httpClient: &http.Client{
			Timeout: time.Second * 10,
		},
	}
}

func (pc *PretiumClient) SetHTTPClient(httpClient *http.Client) *PretiumClient {
	pc.httpClient = httpClient

	return pc
}

func (pc *PretiumClient) TriggerOnramp(ctx context.Context, body apiresp.PretiumTriggerOnrampRequest) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodPost, "trigger-onramp", body)
}

func (pc *PretiumClient) CreateLink(ctx context.Context, body apiresp.PretiumLinkRequest) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodPost, "link", body)
}

func (pc *PretiumClient) GetLinks(ctx context.Context, phoneNumber string) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodGet, fmt.Sprintf("link/%s", phoneNumber), nil)
}

func (pc *PretiumClient) GetTransactions(ctx context.Context, phoneNumber string) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodGet, fmt.Sprintf("transactions/%s", phoneNumber), nil)
}

func (pc *PretiumClient) GetTransactionsByAddress(ctx context.Context, address string) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodGet, fmt.Sprintf("transactions-by-address/%s", address), nil)
}

func (pc *PretiumClient) GetRecentTransactions(ctx context.Context) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodGet, "transactions-recent", nil)
}

func (pc *PretiumClient) GetRates(ctx context.Context) (*OKResponse, int, error) {
	return pc.requestJSON(ctx, http.MethodGet, "rates", nil)
}

func (pc *PretiumClient) setDefaultHeaders(req *http.Request) *http.Request {
	req.Header.Set("Accept", contentType)
	req.Header.Set("Content-Type", contentType)

	return req
}

func (pc *PretiumClient) requestWithCtx(ctx context.Context, method string, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	return pc.do(req)
}

func (pc *PretiumClient) do(req *http.Request) (*http.Response, error) {
	return pc.httpClient.Do(pc.setDefaultHeaders(req))
}

func (pc *PretiumClient) requestJSON(ctx context.Context, method, path string, payload any) (*OKResponse, int, error) {
	var body io.Reader
	if payload != nil {
		buf := bytes.NewBuffer(nil)
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return nil, 0, err
		}
		body = buf
	}

	resp, err := pc.requestWithCtx(ctx, method, pc.endpoint+strings.TrimLeft(path, "/"), body)
	if err != nil {
		return nil, 0, err
	}

	statusCode := resp.StatusCode
	var okResp OKResponse
	if err := parseResponse(resp, &okResp); err != nil {
		return nil, statusCode, err
	}

	return &okResp, statusCode, nil
}

func parseResponse(resp *http.Response, target any) error {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		var apiErr ErrResponse
		if err := json.Unmarshal(b, &apiErr); err == nil && apiErr.Description != "" {
			return &APIError{
				StatusCode:  resp.StatusCode,
				Description: apiErr.Description,
				Body:        string(b),
			}
		}

		description := strings.TrimSpace(string(b))
		if description == "" {
			description = fmt.Sprintf("Pretium server error: status=%s", resp.Status)
		}

		return &APIError{
			StatusCode:  resp.StatusCode,
			Description: description,
			Body:        string(b),
		}
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func (e *APIError) Error() string {
	return fmt.Sprintf("pretium server error: status=%d description=%s", e.StatusCode, e.Description)
}
