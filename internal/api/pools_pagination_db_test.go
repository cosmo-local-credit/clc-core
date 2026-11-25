package api

// A direction parameter is meaningless without an anchor. `next` applies
// to a cursor, so `?next=true` on its own must return the same first page
// a bare request returns — not a silently empty page a cursor loop would
// read as the end of the list. The same normalization guards `next`
// against the zero cursor an explicit `cursor=0` produces.
//
// Verified live against api.cosmolocal.credit before this fix:
// `GET /api/v2/pools?next=true` answered `{"pools":[]}`.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestPoolListNextWithoutCursorReadsAsFirstPage(t *testing.T) {
	a := newMarketAPIWithStore(t)
	ctx := context.Background()

	seedTx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	var seeded []uint64
	for i := range 3 {
		var id uint64
		if err := seedTx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals)
			VALUES (100, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, $2, 6)
			RETURNING id`, fmt.Sprintf("cursorless-next pool %d", i),
			fmt.Sprintf("NCP%d", i)).Scan(&id); err != nil {
			_ = seedTx.Rollback(ctx)
			t.Fatalf("seed pool: %v", err)
		}
		seeded = append(seeded, id)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	defer func() {
		cleanupTx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanupTx.Rollback(ctx)
		for _, id := range seeded {
			if _, err := cleanupTx.Exec(ctx, `DELETE FROM pools WHERE id = $1`, id); err != nil {
				t.Errorf("cleanup pool %d: %v", id, err)
			}
		}
		if err := cleanupTx.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}()

	// The first page is whatever the catalog holds — seeded rows plus any
	// fixtures earlier tests left; the assertion compares pages, not totals.
	firstRes, firstPage := getJSON(t, a, "", "/api/v2/pools?perPage=2")
	if firstRes.Code != http.StatusOK {
		t.Fatalf("first page: status = %d, want %d", firstRes.Code, http.StatusOK)
	}
	bare := firstPage["pools"].([]any)
	if len(bare) != 2 {
		t.Fatalf("first page returned %d rows, want 2", len(bare))
	}

	for _, target := range []string{
		"/api/v2/pools?perPage=2&next=true",
		"/api/v2/pools?perPage=2&next=true&cursor=0",
	} {
		res, page := getJSON(t, a, "", target)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", target, res.Code, http.StatusOK)
		}
		rows, ok := page["pools"].([]any)
		if !ok {
			t.Fatalf("%s: pools is not a list: %#v", target, page["pools"])
		}
		if len(rows) != len(bare) {
			t.Fatalf("%s: returned %d rows, want the first page's %d", target, len(rows), len(bare))
		}
		for i, row := range rows {
			got := row.(map[string]any)["id"]
			want := bare[i].(map[string]any)["id"]
			if got != want {
				t.Fatalf("%s: row %d id = %v, want first-page id %v", target, i, got, want)
			}
		}
	}
}
