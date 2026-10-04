package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Custom vocabulary end to end: creation through the owning write, canonical
// reuse of an existing spelling, the admin listing, and the destructive
// cleanup. Real routes, real auth, real database. Skipped unless
// CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
//
// Every value created here carries the marker below so cleanup cannot reach a
// built-in or another suite's row.
const (
	vocabularyMarker = "Zqvocab"
)

func vocabularyValue(suffix string) string {
	return vocabularyMarker + " " + suffix
}

// deleteVocabulary removes any row this file created, whether the test that
// created it asserted on it or failed first.
func deleteVocabularyFixtures(t *testing.T, a *API) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DELETE FROM offering_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM voucher_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM pool_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM report_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM deployed_token_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM deployed_pool_tags WHERE tag_value ILIKE $1`,
		`DELETE FROM token_tag WHERE value ILIKE $1`,
		`UPDATE offerings SET unit = NULL WHERE unit ILIKE $1`,
		`DELETE FROM offering_unit WHERE value ILIKE $1`,
	} {
		if _, err := a.store.Pool().Exec(ctx, statement, vocabularyMarker+"%"); err != nil {
			t.Errorf("clean vocabulary fixtures: %v", err)
			return
		}
	}
}

func vocabularyExists(t *testing.T, a *API, table, value string) (string, bool, bool) {
	t.Helper()
	var (
		canonical string
		isCustom  bool
	)
	rows, err := a.store.Pool().Query(context.Background(),
		`SELECT value, is_custom FROM `+table+` WHERE lower(value) = lower($1)`, value)
	if err != nil {
		t.Fatalf("read %s %q: %v", table, value, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, false
	}
	if err := rows.Scan(&canonical, &isCustom); err != nil {
		t.Fatalf("scan %s %q: %v", table, value, err)
	}
	return canonical, isCustom, true
}

// A unit nobody seeded becomes a Title Case row the moment the offering that
// names it is written, and the offering stores the canonical spelling.
func TestOfferingWriteCreatesACustomUnitInTitleCase(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token,
		offeringBody(map[string]any{"unit": "  " + strings.ToLower(vocabularyMarker) + "   produce   CRATE  "}))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", res.Code, res.Body.String())
	}

	want := vocabularyValue("Produce Crate")
	if got := offeringFrom(t, result)["unit"]; got != want {
		t.Errorf("unit = %v, want %q", got, want)
	}
	canonical, isCustom, found := vocabularyExists(t, a, "offering_unit", want)
	if !found {
		t.Fatalf("offering_unit has no row for %q", want)
	}
	if canonical != want {
		t.Errorf("stored unit = %q, want %q", canonical, want)
	}
	if !isCustom {
		t.Error("is_custom = false, want true; a value the API created is not a built-in")
	}
}

// "KG" is the built-in "kg" spelled loudly. Resolving it to the existing row is
// what keeps one concept from becoming two vocabulary entries.
func TestOfferingWriteResolvesAnExistingUnitCaseInsensitively(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token,
		offeringBody(map[string]any{"unit": "KG"}))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", res.Code, res.Body.String())
	}
	if got := offeringFrom(t, result)["unit"]; got != "kg" {
		t.Errorf("unit = %v, want the built-in spelling kg", got)
	}

	var extra int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM offering_unit WHERE lower(value) = 'kg'`).Scan(&extra); err != nil {
		t.Fatalf("count kg rows: %v", err)
	}
	if extra != 1 {
		t.Errorf("offering_unit has %d rows for kg, want 1", extra)
	}
}

// An update reaches the same vocabulary path as a create, and the free-text
// unit the Sarafu import left behind is a legal value to keep on the way past.
func TestOfferingUpdateCreatesACustomUnit(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPut,
		offeringItemPath(fx.ownedAddress, fx.legacyOfferingID), token,
		offeringBody(map[string]any{"unit": vocabularyMarker + " hand cart"}))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	want := vocabularyValue("Hand Cart")
	if got := offeringFrom(t, result)["unit"]; got != want {
		t.Errorf("unit = %v, want %q", got, want)
	}
	if _, _, found := vocabularyExists(t, a, "offering_unit", want); !found {
		t.Fatalf("offering_unit has no row for %q", want)
	}
}

func TestOfferingUnitValidation(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	for name, unit := range map[string]string{
		"blank":           "   ",
		"over 32 runes":   vocabularyMarker + " " + strings.Repeat("a", 33),
		"whitespace only": "\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := offeringRequest(t, a, http.MethodPost,
				"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token,
				offeringBody(map[string]any{"unit": unit}))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", res.Code, res.Body.String())
			}
		})
	}

	var leaked int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM offering_unit WHERE value ILIKE $1`, vocabularyMarker+"%").Scan(&leaked); err != nil {
		t.Fatalf("count leaked units: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d custom units survived a rejected write, want 0", leaked)
	}
}

// The voucher profile write is one of the five tag paths. A tag nobody seeded
// is created with the profile, in the same transaction.
func TestVoucherProfileWriteCreatesACustomTag(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fx.ownedAddress+"/profile", token,
		map[string]any{
			"displayName": "Malii Fresh",
			"tags":        []string{strings.ToLower(vocabularyMarker) + " urban   FARMING"},
		})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	want := vocabularyValue("Urban Farming")
	voucher, _ := result["voucher"].(map[string]any)
	tags, _ := voucher["tags"].([]any)
	if len(tags) != 1 || tags[0] != want {
		t.Errorf("tags = %#v, want [%q]", voucher["tags"], want)
	}
	if _, isCustom, found := vocabularyExists(t, a, "token_tag", want); !found || !isCustom {
		t.Errorf("token_tag %q found=%v is_custom=%v, want a custom row", want, found, isCustom)
	}
}

// The same spelling twice is a client mistake, not two tags, and it must not
// reach the relation table as a duplicate key.
func TestVoucherProfileWriteRejectsDuplicateAndMalformedTags(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	for name, tags := range map[string][]string{
		"case-folded duplicate": {vocabularyValue("Fish"), strings.ToUpper(vocabularyValue("Fish"))},
		"comma":                 {vocabularyMarker + " food, water"},
		"blank":                 {"   "},
		"over 100 runes":        {vocabularyMarker + " " + strings.Repeat("a", 101)},
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := offeringRequest(t, a, http.MethodPut,
				"/api/v2/vouchers/"+fx.ownedAddress+"/profile", token,
				map[string]any{"displayName": "Malii Fresh", "tags": tags})
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", res.Code, res.Body.String())
			}
		})
	}
}

// Two writers submitting the same new tag at the same time must end with one
// row. The unique index on lower(value) is what makes that true; the handler's
// find-then-insert alone cannot.
func TestConcurrentWritesCreateOneCustomTag(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	spellings := []string{
		strings.ToLower(vocabularyMarker) + " agroforestry",
		strings.ToUpper(vocabularyMarker) + " AGROFORESTRY",
	}

	var wg sync.WaitGroup
	codes := make([]int, len(spellings))
	for i, spelling := range spellings {
		wg.Go(func() {
			res, _ := offeringRequest(t, a, http.MethodPut,
				"/api/v2/vouchers/"+fx.ownedAddress+"/profile", token,
				map[string]any{"displayName": "Malii Fresh", "tags": []string{spelling}})
			codes[i] = res.Code
		})
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("writer %d status = %d, want 200", i, code)
		}
	}

	var rows int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM token_tag WHERE lower(value) = lower($1)`,
		vocabularyValue("Agroforestry")).Scan(&rows); err != nil {
		t.Fatalf("count agroforestry rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("token_tag has %d rows for the concurrent tag, want 1", rows)
	}
}

// A write that fails after the vocabulary insert must leave no value behind:
// a cancelled or rejected form creates nothing.
func TestARejectedWriteCreatesNoVocabulary(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	stranger := offeringToken(t, priv, offeringStrangerAddr, "USER")
	res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fx.ownedAddress+"/profile", stranger,
		map[string]any{
			"displayName": "Malii Fresh",
			"tags":        []string{vocabularyValue("Ghost")},
		})
	if res.Code == http.StatusOK {
		t.Fatalf("status = 200, want a refusal for a non-owner")
	}
	if _, _, found := vocabularyExists(t, a, "token_tag", vocabularyValue("Ghost")); found {
		t.Error("a refused write left a tag behind")
	}
}

// The public unit list is what the offering form's picker reads, so a custom
// unit has to appear in it for everyone, not only its creator.
func TestOfferingUnitsListingIncludesCustomValues(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	if res, _ := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", owner,
		offeringBody(map[string]any{"unit": vocabularyMarker + " ox cart"})); res.Code != http.StatusCreated {
		t.Fatalf("seed custom unit: status = %d body=%s", res.Code, res.Body.String())
	}

	if res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/offering-units", "", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", res.Code)
	}

	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/offering-units",
		offeringToken(t, priv, offeringStrangerAddr, "USER"), nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	units, _ := result["units"].([]any)
	var sawBuiltIn, sawCustom bool
	for _, unit := range units {
		switch unit {
		case "kg":
			sawBuiltIn = true
		case vocabularyValue("Ox Cart"):
			sawCustom = true
		}
	}
	if !sawBuiltIn || !sawCustom {
		t.Errorf("units = %#v, want both the built-in kg and the custom value", units)
	}
}

func TestAdminVocabularyRequiresSuperadmin(t *testing.T) {
	a, priv := newOfferingAPI(t)

	for name, tc := range map[string]struct {
		token string
		want  int
	}{
		"anonymous": {token: "", want: http.StatusUnauthorized},
		"user":      {token: offeringToken(t, priv, offeringOwnerAddress, "USER"), want: http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			if res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vocabulary", tc.token, nil); res.Code != tc.want {
				t.Errorf("GET status = %d, want %d", res.Code, tc.want)
			}
			res, _ := offeringRequest(t, a, http.MethodDelete, "/api/v2/admin/vocabulary/tags", tc.token,
				map[string]any{"value": vocabularyValue("Nope")})
			if res.Code != tc.want {
				t.Errorf("DELETE status = %d, want %d", res.Code, tc.want)
			}
		})
	}
}

// The admin table exists to answer "what breaks if I remove or rename this",
// so the listing carries the per-resource counts, and flags the built-ins that
// can be renamed but never deleted.
func TestAdminVocabularyListsEveryValueWithUsage(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	if res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fx.ownedAddress+"/profile", owner,
		map[string]any{"displayName": "Malii Fresh", "tags": []string{vocabularyValue("Seedlings")}}); res.Code != http.StatusOK {
		t.Fatalf("seed custom tag: status = %d body=%s", res.Code, res.Body.String())
	}
	if res, _ := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", owner,
		offeringBody(map[string]any{"unit": vocabularyValue("Seed Tray")})); res.Code != http.StatusCreated {
		t.Fatalf("seed custom unit: status = %d body=%s", res.Code, res.Body.String())
	}

	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vocabulary", admin, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	shippedTag := builtInTag(t, a)
	tags, _ := result["tags"].([]any)
	var tag, builtIn map[string]any
	for _, entry := range tags {
		row, _ := entry.(map[string]any)
		switch row["value"] {
		case vocabularyValue("Seedlings"):
			tag = row
		case shippedTag:
			builtIn = row
		}
	}
	if tag == nil {
		t.Fatalf("tags = %#v, want the custom value", tags)
	}
	if tag["isCustom"] != true {
		t.Errorf("custom tag isCustom = %v, want true", tag["isCustom"])
	}
	if builtIn == nil || builtIn["isCustom"] != false {
		t.Errorf("built-in tag %q = %#v, want it listed with isCustom false", shippedTag, builtIn)
	}
	if got := tag["vouchers"]; got != float64(1) {
		t.Errorf("vouchers = %v, want 1", got)
	}
	if got := tag["posts"]; got != float64(0) {
		t.Errorf("posts = %v, want 0", got)
	}

	units, _ := result["units"].([]any)
	var unit, kg map[string]any
	for _, entry := range units {
		row, _ := entry.(map[string]any)
		switch row["value"] {
		case vocabularyValue("Seed Tray"):
			unit = row
		case "kg":
			kg = row
		}
	}
	if unit == nil {
		t.Fatalf("units = %#v, want the custom value", units)
	}
	if unit["isCustom"] != true {
		t.Errorf("custom unit isCustom = %v, want true", unit["isCustom"])
	}
	if kg == nil || kg["isCustom"] != false {
		t.Errorf("built-in unit kg = %#v, want it listed with isCustom false", kg)
	}
	if got := unit["offerings"]; got != float64(1) {
		t.Errorf("offerings = %v, want 1", got)
	}
}

// Deleting a tag clears every relation that referenced it and reports what it
// removed, so the console can describe the damage before and after.
func TestAdminDeleteCustomTagClearsRelations(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	if res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fx.ownedAddress+"/profile", owner,
		map[string]any{"displayName": "Malii Fresh", "tags": []string{vocabularyValue("Beekeeping")}}); res.Code != http.StatusOK {
		t.Fatalf("seed custom tag: status = %d body=%s", res.Code, res.Body.String())
	}

	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	// Case-insensitive: the console sends what the table printed, but an
	// operator typing the value by hand must reach the same row.
	res, result := offeringRequest(t, a, http.MethodDelete, "/api/v2/admin/vocabulary/tags", admin,
		map[string]any{"value": strings.ToUpper(vocabularyValue("Beekeeping"))})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := result["value"]; got != vocabularyValue("Beekeeping") {
		t.Errorf("value = %v, want the canonical spelling", got)
	}
	if got := result["total"]; got != float64(1) {
		t.Errorf("total = %v, want 1", got)
	}

	if _, _, found := vocabularyExists(t, a, "token_tag", vocabularyValue("Beekeeping")); found {
		t.Error("the tag survived its deletion")
	}
	var relations int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM voucher_tags WHERE tag_value ILIKE $1`,
		vocabularyValue("Beekeeping")).Scan(&relations); err != nil {
		t.Fatalf("count voucher_tags: %v", err)
	}
	if relations != 0 {
		t.Errorf("%d voucher_tags rows survived, want 0", relations)
	}

	// The voucher itself is untouched — deleting a tag is not deleting content.
	var vouchers int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM vouchers WHERE lower(address) = lower($1)`, fx.ownedAddress).Scan(&vouchers); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if vouchers != 1 {
		t.Errorf("voucher count = %d, want the voucher left in place", vouchers)
	}

	if res, _ := offeringRequest(t, a, http.MethodDelete, "/api/v2/admin/vocabulary/tags", admin,
		map[string]any{"value": vocabularyValue("Beekeeping")}); res.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", res.Code)
	}
}

// Deleting a unit keeps the offerings that used it and only clears the column;
// an operator tidying a vocabulary must not delete a seller's listing.
func TestAdminDeleteCustomUnitKeepsTheOfferings(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)

	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, created := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", owner,
		offeringBody(map[string]any{"unit": vocabularyValue("Wheelbarrow")}))
	if res.Code != http.StatusCreated {
		t.Fatalf("seed custom unit: status = %d body=%s", res.Code, res.Body.String())
	}
	offeringID := offeringFrom(t, created)["id"]

	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	res, result := offeringRequest(t, a, http.MethodDelete, "/api/v2/admin/vocabulary/units", admin,
		map[string]any{"value": vocabularyValue("Wheelbarrow")})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := result["total"]; got != float64(1) {
		t.Errorf("total = %v, want 1", got)
	}

	if _, _, found := vocabularyExists(t, a, "offering_unit", vocabularyValue("Wheelbarrow")); found {
		t.Error("the unit survived its deletion")
	}
	id, ok := offeringID.(float64)
	if !ok {
		t.Fatalf("offering id = %T, want float64", offeringID)
	}
	var unit *string
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT unit FROM offerings WHERE id = $1`, int64(id)).Scan(&unit); err != nil {
		t.Fatalf("read the offering: %v", err)
	}
	if unit != nil {
		t.Errorf("unit = %q, want NULL after the vocabulary value went away", *unit)
	}
}

// Built-ins are the shipped vocabulary. An operator cleaning up their own
// additions must not be able to remove "kg" out from under every importer.
func TestAdminCannotDeleteBuiltInVocabulary(t *testing.T) {
	a, priv := newOfferingAPI(t)
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")

	for path, value := range map[string]string{
		"/api/v2/admin/vocabulary/units": "kg",
		"/api/v2/admin/vocabulary/tags":  builtInTag(t, a),
	} {
		res, _ := offeringRequest(t, a, http.MethodDelete, path, admin, map[string]any{"value": value})
		if res.Code != http.StatusConflict {
			t.Errorf("DELETE %s status = %d, want 409 body=%s", path, res.Code, res.Body.String())
		}
	}

	var units int64
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM offering_unit WHERE value = 'kg'`).Scan(&units); err != nil {
		t.Fatalf("count kg: %v", err)
	}
	if units != 1 {
		t.Errorf("kg row count = %d, want 1", units)
	}
}

func builtInTag(t *testing.T, a *API) string {
	t.Helper()
	var value string
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT value FROM token_tag WHERE NOT is_custom ORDER BY value LIMIT 1`).Scan(&value); err != nil {
		t.Skipf("token_tag has no built-in values: %v", err)
	}
	return value
}
