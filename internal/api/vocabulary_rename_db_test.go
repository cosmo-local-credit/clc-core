package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

// Uses CORE_TEST_DSN and the existing real HTTP/auth/database harness.
func TestAdminRenameVocabularyPreservesReferences(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	defer deleteVocabularyFixtures(t, a)
	ctx := context.Background()
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	tag, unit := vocabularyValue("Seedlings"), vocabularyValue("Tray")

	res, _ := offeringRequest(t, a, http.MethodPut, "/api/v2/vouchers/"+fx.ownedAddress+"/profile", owner,
		map[string]any{"displayName": "Malii Fresh", "tags": []string{tag}})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	res, created := offeringRequest(t, a, http.MethodPost, "/api/v2/vouchers/"+fx.ownedAddress+"/offerings", owner,
		offeringBody(map[string]any{"unit": unit}))
	require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
	offeringID, ok := offeringFrom(t, created)["id"].(float64)
	require.True(t, ok, "created offering must have a numeric id")
	id := uint64(offeringID)

	for path, old := range map[string]string{"tags": tag, "units": unit} {
		res, result := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/"+path, admin,
			map[string]any{"value": strings.ToUpper(old), "replacement": old + " NEW"})
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Equal(t, old+" NEW", result["value"])
	}
	var storedTag string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT tag_value FROM voucher_tags WHERE voucher_id=$1`, fx.ownedID).Scan(&storedTag))
	require.Equal(t, tag+" NEW", storedTag)
	res, fetched := offeringRequest(t, a, http.MethodGet, offeringItemPath(fx.ownedAddress, id), "", nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, unit+" NEW", offeringFrom(t, fetched)["unit"])

	res, listed := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vocabulary", admin, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	for key, value := range map[string]string{"tags": tag, "units": unit} {
		found := false
		items, ok := listed[key].([]any)
		require.True(t, ok, "%s must be a list", key)
		for _, item := range items {
			row, ok := item.(map[string]any)
			require.True(t, ok, "vocabulary entry must be an object")
			require.NotEqual(t, value, row["value"])
			if row["value"] == value+" NEW" {
				found = true
				countField := "vouchers"
				if key == "units" {
					countField = "offerings"
				}
				require.Equal(t, float64(1), row[countField], "renaming must preserve usage counts")
			}
		}
		require.True(t, found, "renamed %s missing from listing", key)
	}
}

func TestAdminRenameVocabularyOutcomes(t *testing.T) {
	a, priv := newOfferingAPI(t)
	defer deleteVocabularyFixtures(t, a)
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	for _, kind := range []struct {
		path, table string
		max         int
	}{
		{"tags", "token_tag", 100}, {"units", "offering_unit", 32},
	} {
		t.Run(kind.path, func(t *testing.T) {
			old, duplicate := vocabularyValue("Original"), vocabularyValue("Duplicate")
			for _, name := range []string{old, duplicate} {
				_, err := a.store.Pool().Exec(context.Background(), `INSERT INTO `+kind.table+` (value,is_custom) VALUES ($1,true)`, name)
				require.NoError(t, err)
			}
			for _, tc := range []struct {
				name, source, replacement string
				status                    int
			}{
				{"duplicate", old, duplicate, http.StatusConflict},
				{"case folded duplicate", old, strings.ToUpper(duplicate), http.StatusConflict},
				{"missing", vocabularyValue("Missing"), vocabularyValue("New"), http.StatusNotFound},
				{"blank", old, " \t ", http.StatusBadRequest},
				{"too long", old, strings.Repeat("ž", kind.max+1), http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					res, _ := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/"+kind.path, admin,
						map[string]any{"value": tc.source, "replacement": tc.replacement})
					require.Equal(t, tc.status, res.Code, res.Body.String())
					_, custom, found := vocabularyExists(t, a, kind.table, old)
					require.True(t, found && custom, "failed rename must leave source intact")
				})
			}
			// Casing and whitespace normalization; then a repeated identical request.
			current := old
			for _, replacement := range []string{
				strings.ToUpper(old), "  " + vocabularyMarker + "   kWh  ", vocabularyValue("kWh"),
				vocabularyMarker + strings.Repeat("ž", kind.max-len(vocabularyMarker)),
			} {
				res, result := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/"+kind.path, admin,
					map[string]any{"value": current, "replacement": replacement})
				require.Equal(t, http.StatusOK, res.Code, res.Body.String())
				current = strings.Join(strings.Fields(replacement), " ")
				require.Equal(t, current, result["value"])
				stored, custom, found := vocabularyExists(t, a, kind.table, current)
				require.True(t, found && custom)
				require.Equal(t, current, stored)
			}
		})
	}
	for path, value := range map[string]string{"tags": builtInTag(t, a), "units": "kg"} {
		res, _ := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/"+path, admin,
			map[string]any{"value": value, "replacement": vocabularyValue("Protected")})
		require.Equal(t, http.StatusConflict, res.Code, res.Body.String())
	}
}

func TestAdminRenameVocabularyDeploymentJobs(t *testing.T) {
	a, priv := newOfferingAPI(t)
	defer deleteVocabularyFixtures(t, a)
	ctx := context.Background()
	migrator, err := rivermigrate.New(riverpgxv5.New(a.store.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	for _, kind := range []string{"TOKEN_DEPLOY", "POOL_DEPLOY"} {
		for _, state := range []string{"available", "pending", "running", "scheduled", "retryable", "discarded", "completed", "cancelled"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				value := vocabularyValue(kind + " " + state)
				_, err := a.store.Pool().Exec(ctx, `INSERT INTO token_tag(value,is_custom) VALUES ($1,true)`, value)
				require.NoError(t, err)
				args, err := json.Marshal(map[string]any{"tags": []string{value}})
				require.NoError(t, err)
				var id int64
				require.NoError(t, a.store.Pool().QueryRow(ctx, `
					INSERT INTO river_job(kind,args,state,max_attempts,priority,queue,finalized_at)
					VALUES ($1,$2,$3::river_job_state,3,1,'default',CASE WHEN $3::river_job_state IN ('completed','cancelled','discarded') THEN now() END)
					RETURNING id`, kind, args, state).Scan(&id))
				t.Cleanup(func() {
					_, err := a.store.Pool().Exec(ctx, `DELETE FROM river_job WHERE id=$1`, id)
					require.NoError(t, err)
				})
				rename := func() int {
					res, _ := offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vocabulary/tags", admin,
						map[string]any{"value": value, "replacement": value + " New"})
					return res.Code
				}
				if state == "completed" || state == "cancelled" {
					require.Equal(t, http.StatusOK, rename())
					return
				}
				require.Equal(t, http.StatusConflict, rename())
				_, _, found := vocabularyExists(t, a, "token_tag", value)
				require.True(t, found)
				_, err = a.store.Pool().Exec(ctx, `UPDATE river_job SET state='completed',finalized_at=now() WHERE id=$1`, id)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, rename(), "the admin can retry after job completion")
			})
		}
	}
}
