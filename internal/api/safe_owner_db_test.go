package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

type (
	inspectionReader struct {
		state *safe.State
		reads int
	}
)

func (r *inspectionReader) ReadSafeLatest(context.Context, common.Address) (*safe.State, uint64, error) {
	r.reads++
	return r.state, uint64(500 + r.reads), nil
}

func TestPublicOwnerInspectionPersistsCompatibleSignersWithoutSTS(t *testing.T) {
	a, _ := newOfferingAPI(t)
	a.chainID = 42220
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	const owner = "0x9137770000000000000000000000000000000001"
	_, err := a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, fx.ownedID, owner)
	require.NoError(t, err)
	reader := &inspectionReader{state: &safe.State{Owners: []common.Address{common.HexToAddress(offeringOwnerAddress)}}}
	a.safeReader = reader
	t.Cleanup(func() { _, _ = a.store.Pool().Exec(ctx, `DELETE FROM safe_state WHERE safe_address=$1`, owner) })
	response, result := offeringRequest(t, a, http.MethodGet, "/api/v2/owners/"+owner, "", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "safe", result["kind"])
	require.Nil(t, result["threshold"])
	require.Equal(t, []any{strings.ToLower(offeringOwnerAddress)}, result["signers"])
	require.Nil(t, a.safeProxy, "metadata must not require the transaction service")
	var count int
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM safe_signers WHERE safe_address=$1`, owner).Scan(&count))
	require.Equal(t, 1, count)

	// Reinspection refreshes custom contracts even if they omit Safe events.
	reader.state.Owners = []common.Address{common.HexToAddress(offeringStrangerAddr)}
	response, result = offeringRequest(t, a, http.MethodGet, "/api/v2/owners/"+owner, "", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []any{strings.ToLower(offeringStrangerAddr)}, result["signers"])
}

func TestOwnerInspectionDoesNotRegisterArbitraryMultisigs(t *testing.T) {
	a, _ := newOfferingAPI(t)
	a.chainID = 42220
	reader := &inspectionReader{}
	a.safeReader = reader
	response, result := offeringRequest(t, a, http.MethodGet, "/api/v2/owners/0x7153330000000000000000000000000000000001", "", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "unsupported", result["kind"])
	require.Zero(t, reader.reads)
}
