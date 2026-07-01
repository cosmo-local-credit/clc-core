package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func TestCORSPreflightAllowsWildcardPagesPreview(t *testing.T) {
	e := echo.New()
	e.Use(middleware.CORSWithConfig(newCORSConfig([]string{
		"https://staging.cosmolocal.credit",
		"https://*.clc-app.pages.dev",
	})))
	e.POST("/auth/nonce", func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/auth/nonce", nil)
	req.Header.Set(echo.HeaderOrigin, "https://backend-integration.clc-app.pages.dev")
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPost)
	req.Header.Set(echo.HeaderAccessControlRequestHeaders, echo.HeaderContentType)
	res := httptest.NewRecorder()

	e.ServeHTTP(res, req)

	if got := res.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "https://backend-integration.clc-app.pages.dev" {
		t.Fatalf("allow origin = %q, want preview origin", got)
	}
	if got := res.Header().Get(echo.HeaderAccessControlAllowCredentials); got != "true" {
		t.Fatalf("allow credentials = %q, want true", got)
	}
}

func TestCORSPreflightRejectsMultiLabelPagesSubdomain(t *testing.T) {
	e := echo.New()
	e.Use(middleware.CORSWithConfig(newCORSConfig([]string{
		"https://*.clc-app.pages.dev",
	})))
	e.POST("/auth/nonce", func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/auth/nonce", nil)
	req.Header.Set(echo.HeaderOrigin, "https://a.b.clc-app.pages.dev")
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPost)
	req.Header.Set(echo.HeaderAccessControlRequestHeaders, echo.HeaderContentType)
	res := httptest.NewRecorder()

	e.ServeHTTP(res, req)

	if got := res.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "" {
		t.Fatalf("allow origin = %q, want empty", got)
	}
}
