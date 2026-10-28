package api

import (
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

func TestParseReportPageDirection(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		raw       string
		hasCursor bool
		want      store.CatalogPage
		wantErr   bool
	}{
		{name: "no cursor", want: store.CatalogFirstPage},
		{name: "cursor without next", hasCursor: true, want: store.CatalogNextPage},
		{name: "forward", raw: "true", hasCursor: true, want: store.CatalogNextPage},
		{name: "backward", raw: "false", hasCursor: true, want: store.CatalogPrevPage},
		{name: "mixed case", raw: "FALSE", hasCursor: true, want: store.CatalogPrevPage},
		// A direction with no position has no page to name, so it is rejected
		// rather than answered with the first page.
		{name: "next without cursor", raw: "true", wantErr: true},
		{name: "unparseable", raw: "onwards", hasCursor: true, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseReportPageDirection(testCase.raw, testCase.hasCursor)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("parseReportPageDirection() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReportPageDirection() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("direction = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestManagedReportPageResultCarriesBothEdgeCursors(t *testing.T) {
	base := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	result := managedReportPageResult([]store.Report{
		{ID: 9, UpdatedAt: base.Add(time.Hour)},
		{ID: 4, UpdatedAt: base},
	})

	first, ok := result["first"].(map[string]any)
	if !ok || first["id"] != uint64(9) {
		t.Fatalf("first = %+v", result["first"])
	}
	last, ok := result["last"].(map[string]any)
	if !ok || last["id"] != uint64(4) {
		t.Fatalf("last = %+v", result["last"])
	}

	// Null on an empty page: returning the request's own cursor back would let a
	// client page forever against the end of the list.
	empty := managedReportPageResult([]store.Report{})
	if empty["first"] != nil || empty["last"] != nil {
		t.Fatalf("empty page cursors = %+v / %+v", empty["first"], empty["last"])
	}
}

func TestReportManagementRoutesEnforceCredentialKinds(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		SigningKey:   private,
		VerifyingKey: public,
	})

	userToken := mintReportToken(t, private, reportAuthorAddress, "USER")
	adminToken := mintReportToken(t, private, reportAdminAddress, "SUPERADMIN")
	serviceClaims := JWTCustomClaims{
		PublicKey: reportOtherAddress,
		Service:   true,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	serviceToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, serviceClaims).SignedString(private)
	if err != nil {
		t.Fatalf("sign service token: %v", err)
	}

	get := func(path, token string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res.Code
	}

	for _, path := range []string{
		"/api/v2/users/me/reports",
		"/api/v2/users/me/reports/1",
		"/api/v2/admin/reports",
		"/api/v2/admin/reports/1",
	} {
		if status := get(path, ""); status != http.StatusUnauthorized {
			t.Fatalf("GET %s without auth = %d, want 401", path, status)
		}
		if status := get(path, serviceToken); status != http.StatusForbidden {
			t.Fatalf("GET %s with a service token = %d, want 403", path, status)
		}
	}
	for _, path := range []string{"/api/v2/admin/reports", "/api/v2/admin/reports/1"} {
		if status := get(path, userToken); status != http.StatusForbidden {
			t.Fatalf("GET %s as a normal user = %d, want 403", path, status)
		}
	}

	// A privileged JWT is not authoritative by itself. This deliberately
	// store-less API must fail closed before its handlers run.
	if status := get("/api/v2/admin/reports/not-an-id", adminToken); status != http.StatusUnauthorized {
		t.Fatalf("unverifiable admin detail = %d, want 401", status)
	}
	if status := get("/api/v2/admin/reports?perPage=many", adminToken); status != http.StatusUnauthorized {
		t.Fatalf("unverifiable admin list = %d, want 401", status)
	}
	if status := get("/api/v2/users/me/reports?next=true", userToken); status != http.StatusBadRequest {
		t.Fatalf("own list with next and no cursor = %d, want 400", status)
	}
	if status := get("/api/v2/users/me/reports?cursorId=4", userToken); status != http.StatusBadRequest {
		t.Fatalf("own list with half a cursor = %d, want 400", status)
	}
}
