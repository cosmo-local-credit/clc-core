package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
)

func TestNormalizeWantWrite(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	text := "  Meet by the market  "
	image := "  https://storage.example/want.webp  "
	write, err := normalizeWantWrite(wantWriteRequest{
		Name: "  Bicycle repair  ", Description: "  Help repair a wheel  ",
		LocationName: "  Nairobi  ", GeoLat: -1.286, GeoLng: 36.817,
		StartsAt: now, ContactText: &text, ImageURL: &image,
		MarkerIcon: "question", MarkerColor: "orange", Tags: []string{"Repair", "Repair"},
	})
	require.NoError(t, err)
	require.Equal(t, "Bicycle repair", write.Name)
	require.Equal(t, "Meet by the market", *write.ContactText)
	require.Equal(t, "https://storage.example/want.webp", *write.ImageURL)
	require.Equal(t, []string{"Repair"}, write.Tags)

	withoutContact, err := normalizeWantWrite(wantWriteRequest{
		Name: "Need", Description: "Something", LocationName: "Nairobi",
		GeoLat: 0, GeoLng: 0, StartsAt: now,
		MarkerIcon: "question", MarkerColor: "orange",
	})
	require.NoError(t, err)
	require.Zero(t, withoutContact.ContactPermission)
	require.Nil(t, withoutContact.ContactText)

	for name, mutate := range map[string]func(*wantWriteRequest){
		"bad marker":   func(req *wantWriteRequest) { req.MarkerIcon = "voucher" },
		"bad color":    func(req *wantWriteRequest) { req.MarkerColor = "black" },
		"bad location": func(req *wantWriteRequest) { req.GeoLat = 91 },
		"bad image":    func(req *wantWriteRequest) { value := "javascript:alert(1)"; req.ImageURL = &value },
		"long contact": func(req *wantWriteRequest) { value := strings.Repeat("x", 501); req.ContactText = &value },
		"bad expiry": func(req *wantWriteRequest) {
			expires := now
			req.ExpiresAt = &expires
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := wantWriteRequest{
				Name: "Need", Description: "Something", LocationName: "Nairobi",
				GeoLat: 0, GeoLng: 0, StartsAt: now, ContactText: &text,
				MarkerIcon: "question", MarkerColor: "orange",
			}
			mutate(&req)
			_, err := normalizeWantWrite(req)
			require.Error(t, err)
		})
	}
}

func TestValidateWantProfile(t *testing.T) {
	write := store.WantWrite{ContactPermission: store.WantContactEmail | store.WantContactPhone}
	require.NoError(t, validateWantProfile(write, store.User{Email: "person@example.test", Phone: "+254700000000"}))
	require.Error(t, validateWantProfile(write, store.User{Email: "wallet@clc.local", Phone: "+254700000000"}))
	require.Error(t, validateWantProfile(write, store.User{Email: "person@example.test"}))
	require.NoError(t, validateWantProfile(store.WantWrite{}, store.User{}))
}

func TestWantIDParamRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number", strings.Repeat("9", 40)} {
		e := echo.New()
		req := httptest.NewRequest("GET", "/api/v2/wants/"+value, nil)
		res := httptest.NewRecorder()
		ctx := e.NewContext(req, res)
		ctx.SetPathValues(echo.PathValues{{Name: "id", Value: value}})
		_, err := wantIDParam(ctx)
		require.NoError(t, err)
		require.Equal(t, 400, res.Code, res.Body.String())
	}
}
