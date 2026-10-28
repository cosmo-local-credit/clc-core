package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignersListTheirMultisigsThroughHTTP(t *testing.T) {
	a, key := newOfferingAPI(t)
	a.chainID = 42220
	ctx := context.Background()
	const multisig = "0x9749234100000000000000000000000000000002"
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ReplaceSafeSigners(ctx, tx, a.chainID, multisig,
		[]string{offeringOwnerAddress, offeringStrangerAddr}, 2, 300, 0)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM safe_state WHERE safe_address=$1`, strings.ToLower(multisig))
		require.NoError(t, err)
	})

	listed := func(address string) []any {
		t.Helper()
		response, result := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/safes",
			offeringToken(t, key, address, "USER"), nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		safes, ok := result["safes"].([]any)
		require.True(t, ok, response.Body.String())
		return safes
	}

	for _, signer := range []string{offeringOwnerAddress, offeringStrangerAddr} {
		safes := listed(signer)
		require.Len(t, safes, 1, signer)
		entry, ok := safes[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "safe", entry["kind"])
		require.Equal(t, strings.ToLower(multisig), entry["owner"])
		require.EqualValues(t, 2, entry["threshold"])
		require.ElementsMatch(t, []any{strings.ToLower(offeringOwnerAddress), strings.ToLower(offeringStrangerAddr)}, entry["signers"])
	}
	require.Empty(t, listed(offeringOperatorAddr), "someone who signs for nothing gets an empty list")

	response, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/safes", "", nil)
	require.Equal(t, http.StatusUnauthorized, response.Code)
}
