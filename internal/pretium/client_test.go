package pretium

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

func TestTriggerOnrampUsesConfiguredEndpoint(t *testing.T) {
	var (
		gotMethod      string
		gotPath        string
		gotContentType string
		gotBody        map[string]any
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(OKResponse{Ok: true})
	}))
	defer ts.Close()

	client := New(ts.URL + "/api/v1")

	resp, statusCode, err := client.TriggerOnramp(context.Background(), apiresp.PretiumTriggerOnrampRequest{
		Address: "0x123",
		Amount:  20,
		Asset:   "USDC",
	})
	if err != nil {
		t.Fatalf("TriggerOnramp() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/api/v1/trigger-onramp" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v1/trigger-onramp")
	}
	if gotContentType != "application/json" {
		t.Fatalf("content-type = %q, want %q", gotContentType, "application/json")
	}
	if gotBody["address"] != "0x123" {
		t.Fatalf("address = %#v, want %q", gotBody["address"], "0x123")
	}
	if statusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", statusCode, http.StatusOK)
	}
	if !resp.Ok {
		t.Fatalf("ok = %v, want true", resp.Ok)
	}
}

func TestGetRatesReturnsAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrResponse{
			Ok:          false,
			Description: "upstream error",
		})
	}))
	defer ts.Close()

	client := New(ts.URL + "/api/v1")

	_, statusCode, err := client.GetRates(context.Background())
	if err == nil {
		t.Fatal("GetRates() error = nil, want error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T, want *APIError", err)
	}
	if statusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", statusCode, http.StatusBadRequest)
	}
	if apiErr.Description != "upstream error" {
		t.Fatalf("description = %q, want %q", apiErr.Description, "upstream error")
	}
}
