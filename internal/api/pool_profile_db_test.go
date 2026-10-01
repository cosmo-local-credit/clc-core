package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const poolProfileMarker = "Zqpoolprofile"

type (
	poolProfileFixtures struct {
		ownedAddress     string
		ownedID          uint64
		ownedSymbol      string
		otherAddress     string
		ownerlessAddress string
		ids              []int64
	}
)

func seedPoolProfileFixtures(t *testing.T, a *API) (poolProfileFixtures, func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	var fx poolProfileFixtures
	insert := func(owner *string, suffix string) (uint64, string, string) {
		var id uint64
		var address, symbol string
		if err := tx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals, owner_address,
			    description, unit_of_account, email, location_name, geo_lat, geo_lng)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			    $1, 'ZQP' || gen_random_uuid()::TEXT, 6, $2, 'Before', 'KES', 'before@example.org', $3, -1.29, 36.82)
			RETURNING id, address, symbol`, poolProfileMarker+suffix, owner, poolProfileMarker).
			Scan(&id, &address, &symbol); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed pool %q: %v", suffix, err)
		}
		fx.ids = append(fx.ids, int64(id))
		return id, address, symbol
	}

	owner := strings.ToLower(offeringOwnerAddress)
	fx.ownedID, fx.ownedAddress, fx.ownedSymbol = insert(&owner, " Owned")
	otherOwner := strings.ToLower(offeringStrangerAddr)
	_, fx.otherAddress, _ = insert(&otherOwner, " Other")
	_, fx.ownerlessAddress, _ = insert(nil, " Imported")

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return fx, func() {
		cleanup, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanup.Rollback(ctx)
		if _, err := cleanup.Exec(ctx,
			`DELETE FROM pools WHERE id = ANY($1::BIGINT[])`, fx.ids); err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

func TestPoolOwnerCanEditTheWholeProfileWithoutTouchingChainFacts(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedPoolProfileFixtures(t, a)
	defer cleanup()

	var tag string
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag); err != nil {
		t.Skipf("token_tag vocabulary is empty: %v", err)
	}

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPut,
		"/api/v2/pools/"+fixtures.ownedAddress+"/profile", owner,
		map[string]any{
			"displayName":   "  The Last of Us 2  ",
			"description":   "Random bio set by me! Tlous",
			"unitOfAccount": "IDR",
			"email":         "zan@example.org",
			"website":       "https://yok.example",
			"phone":         "+254222111666",
			"terms":         "No NSFW, no wrapins",
			"locationName":  "Garsen, Tana River, Kenya",
			"geoLat":        -2.472812250103445,
			"geoLng":        40.44507372842975,
			"tags":          []string{tag},
		})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if cacheControl := res.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}

	pool, ok := result["pool"].(map[string]any)
	if !ok {
		t.Fatalf("result.pool = %#v, want object", result["pool"])
	}
	for key, want := range map[string]any{
		"name":          "The Last of Us 2",
		"contractName":  poolProfileMarker + " Owned",
		"description":   "Random bio set by me! Tlous",
		"unitOfAccount": "IDR",
		"email":         "zan@example.org",
		"website":       "https://yok.example",
		"phone":         "+254222111666",
		"terms":         "No NSFW, no wrapins",
		"locationName":  "Garsen, Tana River, Kenya",
	} {
		if got := pool[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}

	var contractName, displayName, symbol string
	if err := a.store.Pool().QueryRow(context.Background(), `
		SELECT name, display_name, symbol FROM pools WHERE id = $1`, fixtures.ownedID).
		Scan(&contractName, &displayName, &symbol); err != nil {
		t.Fatalf("read updated pool: %v", err)
	}
	if contractName != poolProfileMarker+" Owned" || displayName != "The Last of Us 2" || symbol != fixtures.ownedSymbol {
		t.Errorf("chain/catalog names or symbol changed unexpectedly: contract=%q display=%q symbol=%q",
			contractName, displayName, symbol)
	}
}

func TestPoolProfileMutationRejectsNonOwnersAndAcceptsSuperadmins(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedPoolProfileFixtures(t, a)
	defer cleanup()

	body := map[string]any{"description": "Allowed only for an owner", "tags": []string{}}
	stranger := offeringToken(t, signingKey, offeringStrangerAddr, "USER")
	res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/pools/"+fixtures.ownedAddress+"/profile", stranger, body)
	if res.Code != http.StatusForbidden {
		t.Errorf("stranger status = %d, want 403 body=%s", res.Code, res.Body.String())
	}

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/pools/"+fixtures.ownedAddress+"/profile", owner,
		map[string]any{"locationName": "Kilifi", "geoLat": -3.5})
	if res.Code != http.StatusBadRequest {
		t.Errorf("half pin status = %d, want 400 body=%s", res.Code, res.Body.String())
	}

	superadmin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")
	for _, address := range []string{fixtures.otherAddress, fixtures.ownerlessAddress} {
		res, _ = offeringRequest(t, a, http.MethodPut,
			"/api/v2/pools/"+address+"/profile", superadmin, body)
		if res.Code != http.StatusOK {
			t.Errorf("superadmin status = %d, want 200 body=%s", res.Code, res.Body.String())
		}
	}
}

// The profile route can set the same override the narrow display-name route
// does, so it has to refuse a label another pool already shows. Both callers go
// through savePoolProfile, which is where the check lives.
func TestPoolProfileMutationRejectsADisplayNameAnotherPoolHolds(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedPoolProfileFixtures(t, a)
	defer cleanup()
	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	target := "/api/v2/pools/" + fixtures.ownedAddress + "/profile"

	res, _ := offeringRequest(t, a, http.MethodPut, target, owner,
		map[string]any{"displayName": "  " + strings.ToLower(poolProfileMarker) + "   other  "})
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", res.Code, res.Body.String())
	}

	// Clearing the override is never a conflict: it falls back to the contract
	// name, which was already unique enough to deploy.
	res, _ = offeringRequest(t, a, http.MethodPut, target, owner,
		map[string]any{"displayName": ""})
	if res.Code != http.StatusOK {
		t.Fatalf("clearing status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	// And the pool's own label does not collide with itself.
	res, _ = offeringRequest(t, a, http.MethodPut, target, owner,
		map[string]any{"displayName": "Zqpoolprofile Fresh Label"})
	if res.Code != http.StatusOK {
		t.Fatalf("first rename status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	res, _ = offeringRequest(t, a, http.MethodPut, target, owner,
		map[string]any{"displayName": "Zqpoolprofile Fresh Label"})
	if res.Code != http.StatusOK {
		t.Fatalf("re-save status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
}
