package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Replays the exact JSON the admin console puts on the wire, captured from the
// real `createAdminClient().updateVoucherProfile` transport rather than written
// by hand here. A body this suite invents proves the handler works on that body;
// only the captured one proves it works on the console's.
const (
	wireAddTags = `{
	  "description": "A basket of fresh food.",
	  "voucherUoa": "KES",
	  "voucherValue": 10,
	  "email": "issuer@example.test",
	  "website": null,
	  "phone": null,
	  "terms": null,
	  "locationName": "Zqvoucherprofile",
	  "geoLat": -3.5107,
	  "geoLng": 39.9093,
	  "tags": ["Agriculture", "Care"]
	}`

	wireClearTags = `{
	  "description": "A basket of fresh food.",
	  "voucherUoa": "KES",
	  "voucherValue": 10,
	  "email": "issuer@example.test",
	  "website": null,
	  "phone": null,
	  "terms": null,
	  "locationName": "Zqvoucherprofile",
	  "geoLat": -3.5107,
	  "geoLng": 39.9093,
	  "tags": []
	}`
)

func storedVoucherTags(t *testing.T, a *API, address string) []string {
	t.Helper()
	rows, err := a.store.Pool().Query(context.Background(), `
		SELECT vt.tag_value
		FROM voucher_tags vt
		JOIN vouchers v ON v.id = vt.voucher_id
		WHERE lower(v.address) = lower($1)
		ORDER BY vt.tag_value`, address)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	defer rows.Close()

	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan tag: %v", err)
		}
		tags = append(tags, tag)
	}
	return tags
}

func putRawProfile(t *testing.T, a *API, token, address, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		"/api/v2/admin/vouchers/"+address+"/profile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.180:5200"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res.Code, res.Body.String()
}

func TestAdminVoucherTagsFromTheConsolesOwnRequestBody(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	before := storedVoucherTags(t, a, address)
	if len(before) != 2 {
		t.Fatalf("fixture tags = %v, want two seeded tags", before)
	}

	code, body := putRawProfile(t, a, token, address, wireAddTags)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	after := storedVoucherTags(t, a, address)
	want := []string{"Agriculture", "Care"}
	if strings.Join(after, ",") != strings.Join(want, ",") {
		t.Errorf("stored tags = %v, want %v", after, want)
	}
	// The response is what seeds the console's cache, so it has to agree with the
	// table or the screen reverts on the next read.
	for _, tag := range want {
		if !strings.Contains(body, `"`+tag+`"`) {
			t.Errorf("response body is missing tag %q: %s", tag, body)
		}
	}
}

func TestAdminVoucherTagsClearedByTheConsolesOwnRequestBody(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	code, body := putRawProfile(t, a, token, address, wireClearTags)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	if after := storedVoucherTags(t, a, address); len(after) != 0 {
		t.Errorf("stored tags = %v, want none", after)
	}
	if !strings.Contains(body, `"tags":[]`) {
		t.Errorf("response body does not report an empty tag set: %s", body)
	}
}
