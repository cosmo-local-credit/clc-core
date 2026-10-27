package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

const (
	callerOwnAddress   = "0x1111111111111111111111111111111111111111"
	callerOtherAddress = "0x2222222222222222222222222222222222222222"
)

func callerContext(callerType, role, address string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	if callerType != "" {
		c.Set("callerType", callerType)
	}
	if role != "" {
		c.Set("callerRole", role)
	}
	if address != "" {
		c.Set("callerAddress", address)
	}
	return c, rec
}

func TestCallerMayActOnAddress(t *testing.T) {
	tests := []struct {
		name       string
		callerType string
		role       string
		caller     string
		target     string
		want       bool
	}{
		{"user on own address", "user", "USER", callerOwnAddress, callerOwnAddress, true},
		{"user on own address in another case", "user", "USER", callerOwnAddress, "0xABCDEF0000000000000000000000000000000001", true},
		{"user on another address", "user", "USER", callerOwnAddress, callerOtherAddress, false},
		{"superadmin on another address", "user", "SUPERADMIN", callerOwnAddress, callerOtherAddress, true},
		{"admin on another address", "user", "ADMIN", callerOwnAddress, callerOtherAddress, true},
		{"service token on any address", "service", "SERVICE", "", callerOtherAddress, true},
		{"anonymous caller", "", "", "", callerOtherAddress, false},
		{"user without an address", "user", "USER", "", callerOtherAddress, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := tt.caller
			if tt.name == "user on own address in another case" {
				caller = "0xabcdef0000000000000000000000000000000001"
			}
			c, _ := callerContext(tt.callerType, tt.role, caller)
			if got := callerMayActOnAddress(c, tt.target); got != tt.want {
				t.Fatalf("callerMayActOnAddress = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequireStaff(t *testing.T) {
	tests := []struct {
		name       string
		callerType string
		role       string
		wantCode   int
		wantNext   bool
	}{
		{"service token", "service", "SERVICE", http.StatusOK, true},
		{"superadmin", "user", "SUPERADMIN", http.StatusOK, true},
		{"admin", "user", "ADMIN", http.StatusOK, true},
		{"ordinary user", "user", "USER", http.StatusForbidden, false},
		{"anonymous", "", "", http.StatusForbidden, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := callerContext(tt.callerType, tt.role, callerOwnAddress)
			called := false
			handler := (&API{}).requireStaff()(func(c *echo.Context) error {
				called = true
				return c.NoContent(http.StatusOK)
			})
			_ = handler(c)
			if called != tt.wantNext {
				t.Fatalf("next called = %v, want %v", called, tt.wantNext)
			}
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestAdministrativeRoleGuards(t *testing.T) {
	tests := []struct {
		name       string
		middleware echo.MiddlewareFunc
		role       string
		wantCode   int
		wantNext   bool
	}{
		{"admin passes admin guard", (&API{}).requireAdmin(), "ADMIN", http.StatusOK, true},
		{"superadmin passes admin guard", (&API{}).requireAdmin(), "SUPERADMIN", http.StatusOK, true},
		{"user fails admin guard", (&API{}).requireAdmin(), "USER", http.StatusForbidden, false},
		{"admin fails superadmin guard", (&API{}).requireSuperAdmin(), "ADMIN", http.StatusForbidden, false},
		{"superadmin passes superadmin guard", (&API{}).requireSuperAdmin(), "SUPERADMIN", http.StatusOK, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := callerContext("user", tt.role, callerOwnAddress)
			called := false
			handler := tt.middleware(func(c *echo.Context) error {
				called = true
				return c.NoContent(http.StatusOK)
			})
			_ = handler(c)
			if called != tt.wantNext {
				t.Fatalf("next called = %v, want %v", called, tt.wantNext)
			}
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}
