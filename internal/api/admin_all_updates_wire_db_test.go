package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every write the admin console performs, replayed from the body its own
// transport produces. Bodies were captured by driving the real sheets against the
// real `createAdminClient`, so nothing here is a body this suite invented.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/

const poolWireMarker = "Zqpoolwire"

func putRaw(t *testing.T, a *API, token, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5300", 100+len(path)%50)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res.Code, res.Body.String()
}

func seedEditablePoolFixture(t *testing.T, a *API) (address string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	var poolID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals, description,
			unit_of_account, email, location_name, geo_lat, geo_lng)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			'Kawangware', 'ZQW', 6, 'A neighbourhood pool.', 'KES',
			'steward@example.test', $1, -1.2833, 36.75)
		RETURNING id, address`, poolWireMarker).Scan(&poolID, &address); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed pool: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO pool_tags (pool_id, tag_value) VALUES ($1, 'Agriculture')`, poolID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed pool tag: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// By id, not by location_name: this endpoint is assign-all, so a request that
	// omits locationName clears the marker the DELETE would have matched.
	return address, func() {
		remove, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer remove.Rollback(ctx)
		for _, statement := range []string{
			`DELETE FROM pool_tags WHERE pool_id = $1`,
			`DELETE FROM pools WHERE id = $1`,
		} {
			if _, err := remove.Exec(ctx, statement, poolID); err != nil {
				t.Errorf("cleanup: %v", err)
				return
			}
		}
		if err := remove.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

func storedPoolTags(t *testing.T, a *API, address string) []string {
	t.Helper()
	rows, err := a.store.Pool().Query(context.Background(), `
		SELECT pt.tag_value FROM pool_tags pt
		JOIN pools p ON p.id = pt.pool_id
		WHERE lower(p.address) = lower($1) ORDER BY pt.tag_value`, address)
	if err != nil {
		t.Fatalf("read pool tags: %v", err)
	}
	defer rows.Close()

	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tags = append(tags, tag)
	}
	return tags
}

// Captured from EditPoolProfileSheet driven against the real admin client.
const poolWireAddTag = `{
  "description": "A neighbourhood pool.",
  "unitOfAccount": "KES",
  "email": "steward@example.test",
  "website": null,
  "phone": null,
  "terms": null,
  "locationName": "Zqpoolwire",
  "geoLat": -1.2833,
  "geoLng": 36.75,
  "tags": ["Agriculture", "Care"]
}`

func TestAdminPoolTagsFromTheConsolesOwnRequestBody(t *testing.T) {
	a, token := newAdminAPI(t)
	address, cleanup := seedEditablePoolFixture(t, a)
	defer cleanup()

	if before := storedPoolTags(t, a, address); strings.Join(before, ",") != "Agriculture" {
		t.Fatalf("fixture tags = %v, want [Agriculture]", before)
	}

	code, body := putRaw(t, a, token,
		"/api/v2/admin/pools/"+address+"/profile", poolWireAddTag)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	after := storedPoolTags(t, a, address)
	if strings.Join(after, ",") != "Agriculture,Care" {
		t.Errorf("stored pool tags = %v, want [Agriculture Care]", after)
	}
	// The response seeds the console's cache, so it must carry the saved set.
	for _, tag := range []string{"Agriculture", "Care"} {
		if !strings.Contains(body, `"`+tag+`"`) {
			t.Errorf("response is missing tag %q: %s", tag, body)
		}
	}
}

// Captured from the account sheet: AdminUserProfile omits the location keys when
// there is no pin, and carries a clearAvatar flag rather than an empty string.
const userWireProfile = `{
  "name": "Zqwire Operator",
  "email": "operator@example.test",
  "phone": "+254700000000",
  "country": "KE",
  "preferredCurrency": "KES",
  "bio": "Neighbourhood organiser",
  "website": "https://operator.example"
}`

const userWireLimits = `{"maxPools": 7, "maxTokens": 9}`

// seedAccountFixtures builds the vouchers, pools and reports an account screen
// reads but no `users` row, because the reads it was written for do not need one.
// A profile write does.
func seedWireUser(t *testing.T, a *API) (address string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	// EIP-55, because that is the only form a blockchain address is ever stored
	// in. adminUserAddress checksums the path param and update-user-profile
	// compares it exactly; a fixture written in any other casing would be testing
	// a row the product cannot produce.
	address = "0x44d573149dB22e759FB9A38Bcc461c12855c2644"

	if _, err := a.store.Pool().Exec(ctx, `
		INSERT INTO users (blockchain_address, name, email, role)
		VALUES ($1, $2, 'zqwire@example.test', 'USER')
		ON CONFLICT (blockchain_address) DO NOTHING`, address, poolWireMarker); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	return address, func() {
		if _, err := a.store.Pool().Exec(ctx,
			`DELETE FROM users WHERE lower(blockchain_address) = lower($1)`, address); err != nil {
			t.Errorf("cleanup user: %v", err)
		}
	}
}

func TestAdminUserWritesFromTheConsolesOwnRequestBodies(t *testing.T) {
	a, token := newAdminAPI(t)
	accountAddress, cleanup := seedWireUser(t, a)
	defer cleanup()

	code, body := putRaw(t, a, token,
		"/api/v2/admin/users/"+accountAddress+"/profile", userWireProfile)
	if code != http.StatusOK {
		t.Fatalf("profile status = %d, body = %s", code, body)
	}
	for _, want := range []string{
		"Zqwire Operator",
		"KES",
		"Neighbourhood organiser",
		"https://operator.example",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("profile response does not echo %q: %s", want, body)
		}
	}

	code, body = putRaw(t, a, token,
		"/api/v2/admin/users/"+accountAddress+"/limits", userWireLimits)
	if code != http.StatusOK {
		t.Fatalf("limits status = %d, body = %s", code, body)
	}

	var maxPools, maxTokens int
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT max_pools, max_tokens FROM users WHERE lower(blockchain_address) = lower($1)`,
		accountAddress).Scan(&maxPools, &maxTokens); err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if maxPools != 7 || maxTokens != 9 {
		t.Errorf("limits = %d/%d, want 7/9", maxPools, maxTokens)
	}
}
