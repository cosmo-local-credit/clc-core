package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func TestPoolVouchersRejectsInvalidAddressOnce(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/pools/not-an-address/vouchers?perPage=20", nil)
	res := httptest.NewRecorder()
	ctx := e.NewContext(req, res)
	ctx.SetPathValues(echo.PathValues{{Name: "address", Value: "not-an-address"}})

	a := &API{}
	if err := a.poolVouchersListHandler(ctx); err != nil {
		t.Fatalf("poolVouchersListHandler() error = %v", err)
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected exactly one JSON response: %v", err)
	}
	if body.ErrCode != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code = %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
	}
}
