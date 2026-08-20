package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v5"
)

func TestPoolPricingValidation(t *testing.T) {
	for _, unit := range []string{"KES", "USD"} {
		if got := poolPricingValidationError(unit, "oracle"); got != "" {
			t.Fatalf("%s oracle validation = %q", unit, got)
		}
	}

	for _, pricing := range []string{"relative", "decimal"} {
		if got := poolPricingValidationError("BAM", pricing); got != "" {
			t.Fatalf("BAM %s validation = %q", pricing, got)
		}
	}

	got := poolPricingValidationError("BAM", "oracle")
	if !strings.Contains(got, "oracle pricing is not available for BAM") ||
		!strings.Contains(got, "choose relative or decimal") {
		t.Fatalf("BAM oracle validation = %q", got)
	}
}

func TestDeployPoolHandlerRejectsBAMOraclePricing(t *testing.T) {
	e := echo.New()
	e.Validator = &Validator{
		ValidatorProvider: validator.New(validator.WithRequiredStructEnabled()),
	}
	body := []byte(`{
		"name":"Sarajevo Pool",
		"symbol":"SAJE",
		"decimals":"6",
		"owner":"0x33a573149db22e759fB9A38Bcc461C12855c2645",
		"unitOfAccount":"BAM",
		"quoterType":"oracle",
		"feePolicyDefault":"0"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/contracts/pool", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	c := e.NewContext(req, recorder)

	requireNoError(t, (&API{}).deployPoolHandler(c))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code = %q", response.Code)
	}
	if !strings.Contains(response.Description, "oracle pricing is not available for BAM") ||
		!strings.Contains(response.Description, "choose relative or decimal") {
		t.Fatalf("description = %q", response.Description)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
