package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// Malformed JSON stops accepted callers at binding, before any database or
// queue access. A 403 here means the handler rejected the caller's authority.
func TestUpgradeAuthorization(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	for name, handler := range map[string]echo.HandlerFunc{
		"token": a.upgradeTokenHandler,
		"pool":  a.upgradePoolHandler,
	} {
		for caller, tc := range map[string]struct {
			role    string
			service bool
			want    int
		}{
			"superadmin":    {"SUPERADMIN", false, http.StatusBadRequest},
			"service":       {"SERVICE", true, http.StatusBadRequest},
			"ordinary user": {"USER", false, http.StatusForbidden},
			"no caller":     {"", false, http.StatusForbidden},
		} {
			t.Run(name+"/"+caller, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))
				req.Header.Set("Content-Type", "application/json")
				res := httptest.NewRecorder()
				c := a.router.NewContext(req, res)
				c.Set("callerRole", tc.role)
				c.Set("service", tc.service)
				if err := handler(c); err != nil {
					a.customHTTPErrorHandler(c, err)
				}
				if res.Code != tc.want {
					t.Fatalf("status = %d, want %d", res.Code, tc.want)
				}
			})
		}
	}
}
