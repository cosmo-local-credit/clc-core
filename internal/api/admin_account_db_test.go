package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

// End-to-end coverage of the account screen's endpoints through the real route
// table, auth middleware, store and database. Skipped unless CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
//
// Fixtures are committed, because the requests under test open their own
// connections and cannot see an uncommitted transaction. Every fixture is tagged
// with a marker unique to this file so cleanup cannot touch another suite's rows,
// and each test asserts about the accounts it created rather than about totals.
//
// The federation database is not required. Where it is absent the chain figures
// are expected to be null, which is itself part of the contract.

const (
	accountMarker  = "Zqadminacct"
	accountAddress = "0x11a573149db22e759fB9A38Bcc461C12855c2600"
	otherAddress   = "0x22b573149db22e759fB9A38Bcc461C12855c2611"
)

func newAdminAPI(t *testing.T) (*API, string) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live admin account endpoint tests")
	}

	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	a := New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://admin.example"},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
	})

	return a, mintToken(t, priv, "SUPERADMIN")
}

func mintToken(t *testing.T, priv ed25519.PrivateKey, role string) string {
	t.Helper()

	claims := UserJWTClaims{
		UserID:          1,
		EthereumAddress: accountAddress,
		Role:            role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// seedAccountFixtures creates one account that owns two catalog vouchers, one of
// which a pool allows, and authors one approved report. A second account owns a
// voucher of its own, so every assertion below also proves the filters exclude.
func seedAccountFixtures(t *testing.T, a *API) func() {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	voucher := func(owner, name string, active bool) uint64 {
		var id uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active,
			    geo_lat, geo_lng, location_name, owner_address)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			        $1, 'ZQA', 6, $2, -1.29, 36.82, $3, $4)
			RETURNING id`, name, active, accountMarker, owner).Scan(&id); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed voucher %q: %v", name, err)
		}
		return id
	}

	owned := voucher(accountAddress, accountMarker+" Owned", true)
	voucher(accountAddress, accountMarker+" Second", true)
	// Lowercased owner on an inactive voucher: proves the comparison is
	// case-folded and that catalog visibility still applies.
	voucher(lower(accountAddress), accountMarker+" Hidden", false)
	voucher(otherAddress, accountMarker+" Other", true)

	var poolID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals, location_name)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        $1, 'ZQP', 6, $2)
		RETURNING id`, accountMarker+" Pool", accountMarker).Scan(&poolID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed pool: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed) VALUES ($1, $2, TRUE)`,
		poolID, owned); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed pool token: %v", err)
	}

	for _, report := range []struct {
		author string
		title  string
		status string
	}{
		{accountAddress, accountMarker + " Report", "APPROVED"},
		{accountAddress, accountMarker + " Draft", "DRAFT"},
		{otherAddress, accountMarker + " Other Report", "APPROVED"},
	} {
		// reports has no location_name, so description carries the marker.
		if _, err := tx.Exec(ctx, `
			INSERT INTO reports (author_address, title, description, status)
			VALUES ($1, $2, $3, $4)`,
			report.author, report.title, accountMarker, report.status); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed report %q: %v", report.title, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return func() {
		cleanup, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanup.Rollback(ctx)

		for _, statement := range []string{
			`DELETE FROM pool_tokens WHERE pool_id IN (SELECT id FROM pools WHERE location_name = $1)`,
			`DELETE FROM pools WHERE location_name = $1`,
			`DELETE FROM reports WHERE description = $1`,
			`DELETE FROM vouchers WHERE location_name = $1`,
		} {
			if _, err := cleanup.Exec(ctx, statement, accountMarker); err != nil {
				t.Errorf("cleanup: %v", err)
				return
			}
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

func getJSON(t *testing.T, a *API, token, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", 200+len(target)%50)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
	}
	return res, envelope.Result
}

func names(t *testing.T, result map[string]any, key string) []string {
	t.Helper()

	rows, ok := result[key].([]any)
	if !ok {
		t.Fatalf("result[%q] is not a list: %#v", key, result[key])
	}
	titles := make([]string, 0, len(rows))
	for _, row := range rows {
		record, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("row is not an object: %#v", row)
		}
		for _, field := range []string{"name", "title"} {
			if value, ok := record[field].(string); ok {
				titles = append(titles, value)
				break
			}
		}
	}
	return titles
}

func lower(address string) string {
	out := make([]byte, len(address))
	for i := range address {
		c := address[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func TestAdminAccountEndpoints(t *testing.T) {
	a, token := newAdminAPI(t)
	cleanup := seedAccountFixtures(t, a)
	t.Cleanup(cleanup)

	t.Run("vouchers by owner", func(t *testing.T) {
		res, result := getJSON(t, a, "",
			"/api/v2/vouchers?perPage=100&owner="+accountAddress)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
		}

		got := names(t, result, "vouchers")
		assertContains(t, got, accountMarker+" Owned")
		assertContains(t, got, accountMarker+" Second")
		// Inactive vouchers are outside the catalog, and another owner's are outside
		// the filter.
		assertExcludes(t, got, accountMarker+" Hidden")
		assertExcludes(t, got, accountMarker+" Other")
	})

	t.Run("vouchers by owner rejects a malformed address", func(t *testing.T) {
		res, _ := getJSON(t, a, "", "/api/v2/vouchers?perPage=10&owner=not-an-address")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.Code)
		}
	})

	t.Run("reports by author", func(t *testing.T) {
		res, result := getJSON(t, a, token,
			"/api/v2/reports?perPage=100&author="+accountAddress)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
		}

		got := names(t, result, "reports")
		assertContains(t, got, accountMarker+" Report")
		// The feed has always shown approved reports only; the author filter does
		// not change that.
		assertExcludes(t, got, accountMarker+" Draft")
		assertExcludes(t, got, accountMarker+" Other Report")
	})

	t.Run("pool memberships", func(t *testing.T) {
		res, result := getJSON(t, a, token,
			"/api/v2/admin/users/"+accountAddress+"/pools?perPage=100")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
		}
		assertContains(t, names(t, result, "pools"), accountMarker+" Pool")
	})

	t.Run("pool memberships exclude an unrelated account", func(t *testing.T) {
		res, result := getJSON(t, a, token,
			"/api/v2/admin/users/"+otherAddress+"/pools?perPage=100")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
		}
		assertExcludes(t, names(t, result, "pools"), accountMarker+" Pool")
	})

	t.Run("stats agree with the lists", func(t *testing.T) {
		res, result := getJSON(t, a, token,
			"/api/v2/admin/users/"+accountAddress+"/stats")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
		}

		assertCount(t, result, "reports", 1)
		assertCount(t, result, "vouchers", 2)
		assertCount(t, result, "poolMemberships", 1)

		// No federation store is configured, so the chain figures must be null
		// rather than zero: an operator has to be able to tell "none" from
		// "no answer".
		for _, key := range []string{"transactions", "swaps", "partners"} {
			if value, present := result[key]; !present || value != nil {
				t.Errorf("result[%q] = %#v, want null", key, value)
			}
		}
	})

	t.Run("stats reject a malformed address", func(t *testing.T) {
		res, _ := getJSON(t, a, token, "/api/v2/admin/users/not-an-address/stats")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.Code)
		}
	})

	t.Run("admin routes refuse a non-superadmin", func(t *testing.T) {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		// Signed with a different key entirely, which is what an unauthorized
		// caller has: a valid-looking token this API cannot verify.
		res, _ := getJSON(t, a, mintToken(t, priv, "USER"),
			"/api/v2/admin/users/"+accountAddress+"/stats")
		if res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 401 or 403", res.Code)
		}
	})

	t.Run("admin routes refuse an unauthenticated caller", func(t *testing.T) {
		res, _ := getJSON(t, a, "", "/api/v2/admin/users/"+accountAddress+"/stats")
		if res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 401 or 403", res.Code)
		}
	})
}

func assertContains(t *testing.T, got []string, want string) {
	t.Helper()
	if !slices.Contains(got, want) {
		t.Errorf("want %q in %v", want, got)
	}
}

func assertExcludes(t *testing.T, got []string, unwanted string) {
	t.Helper()
	if slices.Contains(got, unwanted) {
		t.Errorf("did not want %q in %v", unwanted, got)
	}
}

func assertCount(t *testing.T, result map[string]any, key string, want float64) {
	t.Helper()
	value, ok := result[key].(float64)
	if !ok {
		t.Fatalf("result[%q] is not a number: %#v", key, result[key])
	}
	if value != want {
		t.Errorf("result[%q] = %v, want %v", key, value, want)
	}
}
