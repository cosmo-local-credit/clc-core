package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func contactClearContext() (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	recorder := httptest.NewRecorder()
	context := e.NewContext(httptest.NewRequest(http.MethodPut, "/profile", nil), recorder)
	return context, recorder
}

func TestProtectResourceContactsPreservesLegacyOmissionsAndAllowsExplicitClears(t *testing.T) {
	c, _ := contactClearContext()
	current := resourceContactFields{
		Email: new("issuer@example.org"), Phone: new("+254700000000"), Website: new("https://example.org"),
	}
	got, handled, err := protectResourceContacts(c, current, resourceContactFields{}, nil)
	require.NoError(t, err)
	require.False(t, handled)
	require.Equal(t, current, got)

	c, _ = contactClearContext()
	got, handled, err = protectResourceContacts(c, current, resourceContactFields{}, []string{"email", "website"})
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, got.Email)
	require.Nil(t, got.Website)
	require.Equal(t, current.Phone, got.Phone)
}

func TestProtectResourceContactsRejectsAmbiguousAndContradictoryClears(t *testing.T) {
	current := resourceContactFields{Email: new("issuer@example.org")}

	c, recorder := contactClearContext()
	_, handled, err := protectResourceContacts(c, current,
		resourceContactFields{Email: new("")}, nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, http.StatusConflict, recorder.Code)

	c, recorder = contactClearContext()
	_, handled, err = protectResourceContacts(c, current,
		resourceContactFields{Email: new("next@example.org")}, []string{"email"})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	c, recorder = contactClearContext()
	_, handled, err = protectResourceContacts(c, current,
		resourceContactFields{}, []string{"email", "email"})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
