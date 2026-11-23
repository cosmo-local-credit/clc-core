package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestProfileVisibilityOnlyPreservesLegacyProfileFieldsDB(t *testing.T) {
	a, key := newOfferingAPI(t)
	address := common.HexToAddress(offeringOwnerAddress).Hex()
	ownerToken := offeringToken(t, key, address, "USER")
	adminToken := offeringToken(t, key, offeringOperatorAddr, "ADMIN")
	ctx := context.Background()
	var ownerID uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT id FROM users WHERE blockchain_address=$1`, address).Scan(&ownerID))
	_, err := a.store.Pool().Exec(ctx, `
        UPDATE users SET name='',email='pending+'||lower(blockchain_address)||'@clc.local',
            phone=NULL,country='KE',preferred_currency='KES',bio='Legacy biography',
            website='https://legacy.example.test',location_name='Legacy location',
            geo_lat=-1.286,geo_lng=36.817,avatar_url='https://storage.example.test/legacy.webp'
		WHERE id=$1`, ownerID)
	require.NoError(t, err)

	snapshot := func() string {
		var value string
		require.NoError(t, a.store.Pool().QueryRow(ctx, `
            SELECT row_to_json(profile)::TEXT FROM (
                SELECT name,email,phone,country,preferred_currency,bio,website,
                    location_name,geo_lat,geo_lng,avatar_url
                FROM users WHERE id=$1
            ) profile`, ownerID).Scan(&value))
		return value
	}
	before := snapshot()
	visibility := profileBoundaryVisibility()
	visibility.Enabled = false
	visibility.Avatar = false
	visibility.Activity = true

	res, result := postJSON(t, a, ownerToken, "/auth/profile", map[string]any{
		"visibilityOnly": true,
		"publicProfile":  visibility,
		// Explicit mode ignores stale base fields, even invalid ones, rather than
		// allowing a privacy-only save to erase or replace existing profile data.
		"name": "Attempted overwrite", "email": "not-an-email", "phone": "+1999",
		"bio": "Attempted overwrite", "geoLat": 999, "clearLocation": true,
		"clearAvatar": true,
	})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, before, snapshot())
	require.Equal(t, "", result["name"])
	require.Equal(t, "Legacy biography", result["bio"])
	var enabled, avatar, activity bool
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
        SELECT public_profile_enabled,public_profile_avatar,public_profile_activity
        FROM users WHERE id=$1`, ownerID).Scan(&enabled, &avatar, &activity))
	require.False(t, enabled)
	require.False(t, avatar)
	require.False(t, activity, "legacy activity opt-ins must be ignored")

	res, _ = postJSON(t, a, ownerToken, "/auth/profile", map[string]any{"visibilityOnly": true})
	require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
	require.Equal(t, before, snapshot())
	badType := curatedRequest(a, http.MethodPost, "/auth/profile", ownerToken,
		`{"visibilityOnly":1,"publicProfile":{"enabled":true}}`)
	require.Equal(t, http.StatusBadRequest, badType.Code, badType.Body.String())
	require.Equal(t, before, snapshot())

	adminPath := fmt.Sprintf("/api/v2/admin/users/%s/profile", address)
	adminBody := `{"visibilityOnly":true,"publicProfile":{"enabled":true}}`
	adminResponse := curatedRequest(a, http.MethodPut, adminPath, adminToken, adminBody)
	require.Equal(t, http.StatusBadRequest, adminResponse.Code, adminResponse.Body.String())

	visibility.Enabled = true
	visibility.Name = true
	visibility.Activity = false
	res, _ = postJSON(t, a, ownerToken, "/auth/profile", map[string]any{
		"name": "Completed profile", "email": "completed@example.test",
		"phone": "+254700000099", "country": "KE", "publicProfile": visibility,
	})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var name, email, phone string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
        SELECT name,COALESCE(email,''),COALESCE(phone,'') FROM users WHERE id=$1`, ownerID).
		Scan(&name, &email, &phone))
	require.Equal(t, "Completed profile", name)
	require.Equal(t, "completed@example.test", email)
	require.Equal(t, "+254700000099", phone)
	var publicName bool
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
        SELECT public_profile_enabled,public_profile_name,public_profile_activity
		FROM users WHERE id=$1`, ownerID).Scan(&enabled, &publicName, &activity))
	require.True(t, enabled)
	require.True(t, publicName)
	require.False(t, activity)
}
