package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/stretchr/testify/require"
)

// These are route-to-PostgreSQL checks. The committed, address-isolated
// fixtures are visible to the independent connections used by real handlers.
type (
	publicProfileBoundary struct {
		t                                                     *testing.T
		a                                                     *API
		adminToken                                            string
		owner, viewer                                         msgUser
		voucherID, offerID, poolID, wantID, catalogID, postID int64
		voucher, pool                                         string
		request                                               uint64
	}
)

func newPublicProfileBoundary(t *testing.T) *publicProfileBoundary {
	t.Helper()
	a, admin := newAdminAPI(t)
	f := &publicProfileBoundary{t: t, a: a, adminToken: admin}
	f.owner = f.user("APPROVED")
	f.viewer = f.user("APPROVED")
	ctx, db := t.Context(), a.store.Pool()
	f.voucher, f.pool = randomAddress(t), randomAddress(t)
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO vouchers
        (chain_id,address,owner_address,name,symbol,active)
        VALUES(100,$1,lower($2),'Profile voucher','PBF',true) RETURNING id`, f.voucher, f.owner.Address).Scan(&f.voucherID))
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM offerings WHERE voucher_id=$1`, f.voucherID)
		require.NoError(t, err)
		_, err = db.Exec(context.Background(), `DELETE FROM vouchers WHERE id=$1`, f.voucherID)
		require.NoError(t, err)
	})
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO offerings
        (voucher_id,seller_address,commodity_name,description,quantity,available)
        VALUES($1,$2,'Profile offer','Already public offer',1,true) RETURNING id`, f.voucherID, f.owner.Address).Scan(&f.offerID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO pools
        (chain_id,address,owner_address,name,symbol,active,geo_lat,geo_lng,location_name)
        VALUES(100,$1,lower($2),'Profile pool','PBFPOOL',true,-3.67891234,39.83456789,'Fractional location') RETURNING id`, f.pool, f.owner.Address).Scan(&f.poolID))
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM pools WHERE id=$1`, f.poolID)
		require.NoError(t, err)
	})
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO wants
        (owner_user_id,name,description,location_name,geo_lat,geo_lng,starts_at,status)
        VALUES($1,'Profile Want','Already public Want','Fractional location',-3.67891234,39.83456789,now()-interval '1 day','OPEN') RETURNING id`, f.owner.ID).Scan(&f.wantID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO catalogs
        (owner_user_id,name,description,location_name,geo_lat,geo_lng)
        VALUES($1,'Profile catalog','Already public catalog','Fractional location',-3.67891234,39.83456789) RETURNING id`, f.owner.ID).Scan(&f.catalogID))
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO reports(author_address,title,description,status)
        VALUES($1,'Profile Post','Public Post with private authorship','APPROVED') RETURNING id`, f.owner.Address).Scan(&f.postID))
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM reports WHERE id=$1`, f.postID)
		require.NoError(t, err)
	})
	return f
}

func (f *publicProfileBoundary) user(status string) msgUser {
	f.t.Helper()
	address := randomAddress(f.t)
	var id int64
	require.NoError(f.t, f.a.store.Pool().QueryRow(f.t.Context(), `INSERT INTO users
        (blockchain_address,username,name,email,phone,country,bio,website,location_name,geo_lat,geo_lng,avatar_url,role,status)
        VALUES($1,$2,'Private name',$3,$4,'KE','Private biography','https://profile.example','Private location',-3.67891234,39.83456789,'https://profile.example/avatar.webp','USER',$5) RETURNING id`,
		address, "profile-"+strings.ToLower(address[2:14]), strings.ToLower(address)+"@example.test", profileBoundaryPhone(address), status).Scan(&id))
	f.t.Cleanup(func() {
		db, ctx := f.a.store.Pool(), context.Background()
		_, err := db.Exec(ctx, `DELETE FROM address_rotations WHERE user_id=$1`, id)
		require.NoError(f.t, err)
		_, err = db.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
		require.NoError(f.t, err)
	})
	token, err := f.a.generateUserToken(store.User{ID: uint64(id), EthereumAddress: address, Role: "USER"})
	require.NoError(f.t, err)
	return msgUser{ID: id, Address: address, Token: token}
}

func (f *publicProfileBoundary) get(path, token string) *httptest.ResponseRecorder {
	f.t.Helper()
	f.request++
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5300", f.request%250+1)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: token})
	}
	res := httptest.NewRecorder()
	f.a.router.ServeHTTP(res, req)
	return res
}

func profileBoundaryPhone(address string) string {
	value, _ := strconv.ParseUint(address[2:10], 16, 32)
	return fmt.Sprintf("+254%010d", value)
}

func profileBoundaryVisibility() apiresp.ProfileVisibility {
	return apiresp.ProfileVisibility{Enabled: true, Avatar: true, Vouchers: true, Offers: true, Wants: true, Pools: true, Catalogs: true}
}

func (f *publicProfileBoundary) publish(visibility apiresp.ProfileVisibility) {
	f.t.Helper()
	res, _ := postJSON(f.t, f.a, f.owner.Token, "/auth/profile", map[string]any{
		"name": "Private name", "email": strings.ToLower(f.owner.Address) + "@example.test", "country": "KE", "phone": profileBoundaryPhone(f.owner.Address), "publicProfile": visibility,
	})
	require.Equal(f.t, http.StatusOK, res.Code, res.Body.String())
}

func profileBoundaryItems(t *testing.T, res *httptest.ResponseRecorder) []store.PublicProfileResource {
	t.Helper()
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
	var body struct {
		Result struct {
			Items []store.PublicProfileResource `json:"items"`
			Next  *string                       `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Result.Items
}

func TestPublicProfileBoundaryConsentAndAuthentication(t *testing.T) {
	f := newPublicProfileBoundary(t)
	path := "/api/v2/profiles/" + f.owner.Address
	anonymous := f.get(path, "")
	require.Equal(t, http.StatusOK, anonymous.Code, anonymous.Body.String())
	require.Equal(t, "no-store", anonymous.Header().Get("Cache-Control"))
	header := resourceResultObject(t, anonymous, "profile")
	for _, key := range []string{"email", "phone", "name", "bio", "website", "country", "locationName", "geoLat", "geoLng", "preferredCurrency", "role", "status", "id"} {
		require.NotContains(t, header, key)
	}
	require.Equal(t, json.RawMessage("false"), header["hasPrivateDetails"])
	for _, cookie := range []string{f.viewer.Token, "forged-session"} {
		withCookie := f.get(path, cookie)
		require.Equal(t, anonymous.Code, withCookie.Code, withCookie.Body.String())
		require.JSONEq(t, anonymous.Body.String(), withCookie.Body.String())
	}
	require.Equal(t, http.StatusUnauthorized, f.get(path+"/details", "").Code)
	require.Equal(t, http.StatusUnauthorized, f.get(path+"/details", "forged-session").Code)
	empty := f.get(path+"/details", f.viewer.Token)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	require.Equal(t, "private, no-store", empty.Header().Get("Cache-Control"))
	require.Empty(t, resourceResultObject(t, empty, "details"))

	visibility := profileBoundaryVisibility()
	visibility.Name, visibility.Bio, visibility.Website, visibility.Email = true, true, true, true
	visibility.Phone, visibility.Country, visibility.LocationName, visibility.PreciseLocation = true, true, true, true
	f.publish(visibility)
	published := f.get(path, "")
	header = resourceResultObject(t, published, "profile")
	require.Equal(t, json.RawMessage(`"Private name"`), header["name"])
	require.Equal(t, json.RawMessage(`"Private biography"`), header["bio"])
	require.Equal(t, json.RawMessage("true"), header["hasPrivateDetails"])
	for _, key := range []string{"email", "phone", "locationName", "geoLat", "geoLng"} {
		require.NotContains(t, header, key)
	}
	details := resourceResultObject(t, f.get(path+"/details", f.viewer.Token), "details")
	require.Contains(t, details, "email")
	require.Equal(t, `"`+profileBoundaryPhone(f.owner.Address)+`"`, string(details["phone"]))
	require.Contains(t, details, "locationName")
	var lat, lng float64
	require.NoError(t, json.Unmarshal(details["geoLat"], &lat))
	require.NoError(t, json.Unmarshal(details["geoLng"], &lng))
	require.Equal(t, -3.67891234, lat)
	require.Equal(t, 39.83456789, lng)
	key, ok := f.a.signingKey.(ed25519.PrivateKey)
	require.True(t, ok)
	service := resourceGet(t, f.a, path+"/details", offeringServiceToken(t, key))
	require.Equal(t, http.StatusForbidden, service.Code, service.Body.String())
	withCookie := f.get(path, f.viewer.Token)
	require.JSONEq(t, published.Body.String(), withCookie.Body.String())

	// Generated sign-in email placeholders never become public contacts.
	visibility = profileBoundaryVisibility()
	visibility.Avatar = false
	visibility.Email = true
	f.publish(visibility)
	_, err := f.a.store.Pool().Exec(t.Context(), `UPDATE users SET email=$2 WHERE id=$1`, f.owner.ID, "pending+"+strings.ToLower(f.owner.Address[2:])+"@clc.local")
	require.NoError(t, err)
	header = resourceResultObject(t, f.get(path, ""), "profile")
	require.NotContains(t, header, "avatarUrl")
	require.Equal(t, json.RawMessage("false"), header["hasPrivateDetails"])
	require.Empty(t, resourceResultObject(t, f.get(path+"/details", f.viewer.Token), "details"))
}

func TestPublicProfileBoundarySectionsAndPagination(t *testing.T) {
	f := newPublicProfileBoundary(t)
	root := "/api/v2/profiles/" + f.owner.Address
	visibility := profileBoundaryVisibility()
	visibility.Posts = true
	f.publish(visibility)
	for _, tc := range []struct {
		section  string
		resource string
		disable  func(*apiresp.ProfileVisibility)
	}{
		{"vouchers", f.voucher, func(v *apiresp.ProfileVisibility) { v.Vouchers = false }},
		{"offers", f.voucher, func(v *apiresp.ProfileVisibility) { v.Offers = false }},
		{"wants", strconv.FormatInt(f.wantID, 10), func(v *apiresp.ProfileVisibility) { v.Wants = false }},
		{"pools", f.pool, func(v *apiresp.ProfileVisibility) { v.Pools = false }},
		{"posts", strconv.FormatInt(f.postID, 10), func(v *apiresp.ProfileVisibility) { v.Posts = false }},
		{"catalogs", strconv.FormatInt(f.catalogID, 10), func(v *apiresp.ProfileVisibility) { v.Catalogs = false }},
	} {
		t.Run(tc.section, func(t *testing.T) {
			items := profileBoundaryItems(t, f.get(root+"/"+tc.section, ""))
			require.Len(t, items, 1)
			require.Equal(t, tc.resource, items[0].ResourceID)
			// A no-location Voucher and fractional-location Pool are included;
			// profile sections have no map-radius or location eligibility rule.
			require.Equal(t, "direct", items[0].Management)
			blocked := visibility
			tc.disable(&blocked)
			f.publish(blocked)
			res := f.get(root+"/"+tc.section, f.viewer.Token)
			require.Equal(t, http.StatusNotFound, res.Code, res.Body.String())
			require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
			f.publish(visibility)
		})
	}
	var second int64
	require.NoError(t, f.a.store.Pool().QueryRow(t.Context(), `INSERT INTO offerings(voucher_id,seller_address,commodity_name,quantity,available) VALUES($1,$2,'Second offer',1,true) RETURNING id`, f.voucherID, f.owner.Address).Scan(&second))
	first := f.get(root+"/offers?perPage=1", "")
	var envelope struct {
		Result struct {
			Items []store.PublicProfileResource `json:"items"`
			Next  *string                       `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &envelope))
	require.Len(t, envelope.Result.Items, 1)
	require.Equal(t, f.voucher, envelope.Result.Items[0].ResourceID)
	require.NotNil(t, envelope.Result.Next)
	require.Equal(t, strconv.FormatInt(second, 10), *envelope.Result.Next)
	next := f.get(root+"/offers?perPage=1&cursor="+*envelope.Result.Next, "")
	require.NoError(t, json.Unmarshal(next.Body.Bytes(), &envelope))
	require.Len(t, envelope.Result.Items, 1)
	require.Equal(t, uint64(f.offerID), envelope.Result.Items[0].ID)
	require.Nil(t, envelope.Result.Next)
	_, err := f.a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET active=false WHERE id=$1`, f.voucherID)
	require.NoError(t, err)
	_, err = f.a.store.Pool().Exec(t.Context(), `UPDATE pools SET active=false WHERE id=$1`, f.poolID)
	require.NoError(t, err)
	for _, section := range []string{"vouchers", "offers", "pools"} {
		require.Empty(t, profileBoundaryItems(t, f.get(root+"/"+section, "")), "retired resources are hidden")
	}
}

func TestPublicProfileBoundaryPostAuthorshipAndOwnerOnlySettings(t *testing.T) {
	f := newPublicProfileBoundary(t)
	root := "/api/v2/profiles/" + f.owner.Address
	postPath := "/api/v2/profiles/resource/post/" + strconv.FormatInt(f.postID, 10)
	require.Equal(t, http.StatusNotFound, f.get(root+"/posts", "").Code)
	hidden := f.get(postPath, "")
	require.Equal(t, http.StatusOK, hidden.Code, hidden.Body.String())
	require.Empty(t, resourceResultObjects(t, hidden, "profiles"))
	visibility := profileBoundaryVisibility()
	visibility.Posts = true
	f.publish(visibility)
	linked := f.get(postPath, "")
	profiles := resourceResultObjects(t, linked, "profiles")
	require.Len(t, profiles, 1)
	require.Equal(t, `"`+f.owner.Address+`"`, string(profiles[0]["address"]))

	// Old and admin correction forms that omit preferences preserve consent.
	res, _ := postJSON(t, f.a, f.owner.Token, "/auth/profile", map[string]any{"name": "Corrected name", "email": strings.ToLower(f.owner.Address) + "@example.test"})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	code, body := putRaw(t, f.a, f.adminToken, "/api/v2/admin/users/"+f.owner.Address+"/profile", fmt.Sprintf(`{"name":"Admin correction","email":%q}`, strings.ToLower(f.owner.Address)+"@example.test"))
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, profileBoundaryItems(t, f.get(root+"/posts", "")), 1)
	code, body = putRaw(t, f.a, f.adminToken, "/api/v2/admin/users/"+f.owner.Address+"/profile", `{"name":"Admin correction","publicProfile":{"enabled":true,"posts":false,"email":true}}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Len(t, profileBoundaryItems(t, f.get(root+"/posts", "")), 1)
	details := resourceResultObject(t, f.get(root+"/details", f.viewer.Token), "details")
	require.NotContains(t, details, "email")
}

func TestPublicProfileBoundaryLifecycleAndRotation(t *testing.T) {
	f := newPublicProfileBoundary(t)
	root := "/api/v2/profiles/" + f.owner.Address
	db, ctx := f.a.store.Pool(), t.Context()
	for _, status := range []string{"PENDING", "REJECTED"} {
		_, err := db.Exec(ctx, `UPDATE users SET status=$2 WHERE id=$1`, f.owner.ID, status)
		require.NoError(t, err)
		for _, path := range []string{root, root + "/vouchers", root + "/details"} {
			response := f.get(path, f.viewer.Token)
			require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		}
	}
	_, err := db.Exec(ctx, `UPDATE users SET status='APPROVED' WHERE id=$1`, f.owner.ID)
	require.NoError(t, err)
	visibility := profileBoundaryVisibility()
	visibility.Enabled = false
	f.publish(visibility)
	require.Equal(t, http.StatusNotFound, f.get(root, "").Code)
	require.Equal(t, http.StatusNotFound, f.get(root+"/vouchers", "").Code)
	visibility.Enabled = true
	f.publish(visibility)
	_, err = db.Exec(ctx, `INSERT INTO account_retirements(user_id,closes_at,request_source) VALUES($1,now()+interval '6 months','SELF')`, f.owner.ID)
	require.NoError(t, err)
	for _, state := range []string{"RETIREMENT_SCHEDULED", "ERASURE_PENDING", "ERASED"} {
		_, err = db.Exec(ctx, `UPDATE account_retirements SET state=$2 WHERE user_id=$1`, f.owner.ID, state)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, f.get(root, "").Code)
		require.Equal(t, http.StatusNotFound, f.get(root+"/vouchers", "").Code)
	}
	_, err = db.Exec(ctx, `DELETE FROM account_retirements WHERE user_id=$1`, f.owner.ID)
	require.NoError(t, err)
	replacement := randomAddress(t)
	_, err = db.Exec(ctx, `UPDATE users SET blockchain_address=$2 WHERE id=$1`, f.owner.ID, replacement)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO address_rotations(user_id,old_address,new_address,initiated_by) VALUES($1,$2,$3,'user')`, f.owner.ID, f.owner.Address, replacement)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, f.get(root, "").Code)
	require.Equal(t, http.StatusOK, f.get("/api/v2/profiles/"+replacement, "").Code)
}

func TestPublicProfileBoundaryCurrentSafeOwnership(t *testing.T) {
	f := newPublicProfileBoundary(t)
	db, ctx := f.a.store.Pool(), t.Context()
	safe := randomAddress(t)
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM safe_state WHERE safe_address=lower($1)`, safe)
		require.NoError(t, err)
	})
	for _, table := range []string{"vouchers", "pools"} {
		address := f.voucher
		if table == "pools" {
			address = f.pool
		}
		_, err := db.Exec(ctx, `UPDATE `+table+` SET owner_address=lower($2) WHERE address=$1`, address, safe)
		require.NoError(t, err)
	}
	replace := func(owners []string, block uint64) {
		tx, err := db.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)
		_, err = f.a.store.ReplaceSafeSigners(ctx, tx, 100, safe, owners, 1, block, 1)
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))
	}
	replace([]string{f.owner.Address, f.viewer.Address}, 1)
	for _, table := range []string{"vouchers", "pools"} {
		for _, user := range []msgUser{f.owner, f.viewer} {
			rows := profileBoundaryItems(t, f.get("/api/v2/profiles/"+user.Address+"/"+table, ""))
			require.Len(t, rows, 1)
			require.Equal(t, "multisig", rows[0].Management)
		}
	}
	replace([]string{f.viewer.Address}, 2)
	for _, table := range []string{"vouchers", "pools"} {
		require.Empty(t, profileBoundaryItems(t, f.get("/api/v2/profiles/"+f.owner.Address+"/"+table, "")))
		address := f.voucher
		if table == "pools" {
			address = f.pool
		}
		_, err := db.Exec(ctx, `UPDATE `+table+` SET owner_address=lower($2) WHERE address=$1`, address, f.owner.Address)
		require.NoError(t, err)
		require.Empty(t, profileBoundaryItems(t, f.get("/api/v2/profiles/"+f.viewer.Address+"/"+table, "")))
		rows := profileBoundaryItems(t, f.get("/api/v2/profiles/"+f.owner.Address+"/"+table, ""))
		require.Len(t, rows, 1)
		require.Equal(t, "direct", rows[0].Management)
	}
}

func TestRetiredPublicProfileActivityHTTPBoundaryDB(t *testing.T) {
	f := newPublicProfileBoundary(t)
	ctx, db := t.Context(), f.a.store.Pool()
	// Historical opt-ins must be suppressed before the account next saves.
	_, err := db.Exec(ctx, `UPDATE users SET public_profile_activity=true WHERE id=$1`, f.owner.ID)
	require.NoError(t, err)
	path := "/api/v2/profiles/" + f.owner.Address
	for _, token := range []string{"", f.viewer.Token, "forged-session"} {
		res := f.get(path, token)
		header := resourceResultObject(t, res, "profile")
		var sections map[string]bool
		require.NoError(t, json.Unmarshal(header["sections"], &sections))
		require.Contains(t, sections, "activity", "cached clients require the compatibility field")
		require.False(t, sections["activity"])
		require.True(t, sections["vouchers"])
	}
	own := resourceResultObject(t, f.get("/auth/profile", f.owner.Token), "publicProfile")
	require.Equal(t, json.RawMessage("false"), own["activity"])
	visibility := profileBoundaryVisibility()
	visibility.Activity, visibility.Posts = true, true
	f.publish(visibility)
	var activity, posts bool
	require.NoError(t, db.QueryRow(ctx, `SELECT public_profile_activity,public_profile_posts FROM users WHERE id=$1`, f.owner.ID).Scan(&activity, &posts))
	require.False(t, activity)
	require.True(t, posts, "remaining publication preferences must still save")

	// Removing both stores proves the tombstone route has no database dependency.
	originalStore, originalFederation := f.a.store, f.a.federationStore
	f.a.store, f.a.federationStore = nil, nil
	defer func() { f.a.store, f.a.federationStore = originalStore, originalFederation }()
	key, ok := f.a.signingKey.(ed25519.PrivateKey)
	require.True(t, ok)
	for _, token := range []string{"", f.owner.Token, f.viewer.Token, "forged-session", offeringServiceToken(t, key)} {
		res := f.get(path+"/activity?perPage=20", token)
		require.Equal(t, http.StatusNotFound, res.Code, res.Body.String())
		require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
		require.NotContains(t, res.Body.String(), "items")
	}
}
