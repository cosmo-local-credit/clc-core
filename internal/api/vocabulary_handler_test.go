package api

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// A nil store makes these tests fail if malformed input reaches the database.
func TestRenameVocabularyRejectsInvalidInput(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	for _, tc := range []struct {
		name, body string
		tag        bool
	}{
		{"malformed JSON", `{`, true},
		{"missing source", `{"replacement":"New"}`, true},
		{"blank source", `{"value":"  ","replacement":"New"}`, false},
		{"missing replacement", `{"value":"Old"}`, true},
		{"blank replacement", `{"value":"Old","replacement":" \t\n "}`, false},
		{"wrong type", `{"value":"Old","replacement":42}`, true},
		{"comma in tag", `{"value":"Old","replacement":"food, water"}`, true},
		{"long tag", `{"value":"Old","replacement":"` + strings.Repeat("ž", 101) + `"}`, true},
		{"long unit", `{"value":"Old","replacement":"` + strings.Repeat("ž", 33) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			// The shared bind/validation helpers write the response and return
			// the original error for logging.
			_ = a.renameVocabulary(a.router.NewContext(req, res), tc.tag)
			require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
			var envelope apiresp.ErrResponse
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
			require.False(t, envelope.Ok)
			require.Contains(t, []string{apiresp.ErrCodeInvalidJSON, apiresp.ErrCodeValidationFailed}, envelope.ErrCode)
		})
	}
}

func TestRenameVocabularyRequiresSuperadmin(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler), SigningKey: priv, VerifyingKey: pub})
	for _, path := range []string{"tags", "units"} {
		for _, tc := range []struct {
			name, token string
			status      int
		}{
			{"anonymous", "", http.StatusUnauthorized},
			{"user", offeringToken(t, priv, offeringOwnerAddress, "USER"), http.StatusForbidden},
			{"service", offeringServiceToken(t, priv), http.StatusForbidden},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				res, _ := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/"+path, tc.token,
					map[string]any{"value": "Old", "replacement": "New"})
				require.Equal(t, tc.status, res.Code, res.Body.String())
			})
		}
	}
}

func TestVocabularyDuplicateErrors(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	for _, constraint := range []string{"token_tag_pkey", "token_tag_value_lower_idx", "offering_unit_pkey", "offering_unit_value_lower_idx"} {
		t.Run(constraint, func(t *testing.T) {
			res := httptest.NewRecorder()
			ctx := a.router.NewContext(httptest.NewRequest(http.MethodPut, "/", nil), res)
			err := fmt.Errorf("scan: %w", &pgconn.PgError{Code: pgUniqueViolation, ConstraintName: constraint})
			require.NoError(t, handlePostgresError(ctx, err))
			require.Equal(t, http.StatusConflict, res.Code)
			var envelope apiresp.ErrResponse
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
			require.Equal(t, apiresp.ErrCodeConflict, envelope.ErrCode)
			require.Contains(t, envelope.Description, "already exists")
		})
	}
	for _, err := range []error{
		errors.New("unavailable"),
		&pgconn.PgError{Code: pgUniqueViolation, ConstraintName: "unrelated_key"},
		&pgconn.PgError{Code: "23503", ConstraintName: "offering_unit_pkey"},
	} {
		res := httptest.NewRecorder()
		ctx := a.router.NewContext(httptest.NewRequest(http.MethodPut, "/", nil), res)
		require.ErrorIs(t, handlePostgresError(ctx, err), err)
		require.Empty(t, res.Body.String())
	}
}
