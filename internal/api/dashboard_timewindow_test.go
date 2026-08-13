package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func TestParseDashboardTimeWindowPreset(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?range=1h", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	window, err := parseDashboardTimeWindow(c)
	if err != nil {
		t.Fatal(err)
	}
	if window.Preset != timeRange1h {
		t.Fatalf("preset: got %q", window.Preset)
	}
	if window.Bucket != 5*time.Minute {
		t.Fatalf("bucket: got %s", window.Bucket)
	}
	span := window.Until.Sub(window.Since)
	if span < 59*time.Minute || span > 61*time.Minute {
		t.Fatalf("span: got %s", span)
	}
}

func TestParseDashboardTimeWindowCustom(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?range=custom&since=2026-08-01T00:00:00Z&until=2026-08-02T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	window, err := parseDashboardTimeWindow(c)
	if err != nil {
		t.Fatal(err)
	}
	if window.Preset != timeRangeCustom {
		t.Fatalf("preset: got %q", window.Preset)
	}
	if window.Until.Sub(window.Since) != 24*time.Hour {
		t.Fatalf("span: got %s", window.Until.Sub(window.Since))
	}
}

func TestParseDashboardTimeWindowRejectsInvertedCustom(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?range=custom&since=2026-08-02T00:00:00Z&until=2026-08-01T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if _, err := parseDashboardTimeWindow(c); err == nil {
		t.Fatal("expected inverted custom range to fail")
	}
}

func TestAdminDashboardStatsHandlerRejectsInvalidRange(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?range=invalid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := (&API{}).adminDashboardStatsHandler(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrCode != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code: got %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
	}
}
