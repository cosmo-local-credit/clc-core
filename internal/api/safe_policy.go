package api

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	// storeSafeResolver answers policy questions from the signer cache and the
	// catalog. Every method reads in its own short transaction and returns an
	// error on any failure, which Check treats as a rejection.
	storeSafeResolver struct{ a *API }
)

func (r storeSafeResolver) IsSafe(ctx context.Context, address common.Address) (bool, error) {
	tx, err := r.a.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	_, err = r.a.store.GetSafeState(ctx, tx, r.a.chainID, address.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r storeSafeResolver) ManagesAsset(ctx context.Context, safeAddress, target common.Address) (bool, error) {
	tx, err := r.a.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	return r.a.store.SafeManagesAsset(ctx, tx, r.a.chainID, safeAddress.Hex(), target.Hex())
}

// newSafePolicy returns nil when Safe execution is not configured; relayHandler
// then rejects any execTransaction batch instead of relaying it unchecked.
func (a *API) newSafePolicy(multiSend []string) *safe.Policy {
	if len(multiSend) == 0 {
		return nil
	}
	addresses := make([]common.Address, 0, len(multiSend))
	for _, s := range multiSend {
		addresses = append(addresses, common.HexToAddress(s))
	}
	return safe.NewPolicy(storeSafeResolver{a: a}, addresses, safe.DefaultAllowedSignatures)
}

// enforceSafePolicy runs the execution-time control on a relay request and
// writes the response itself when it rejects. handled reports that it did.
func (a *API) enforceSafePolicy(c *echo.Context, req apiresp.RelayRequest) (handled bool, err error) {
	calls := make([]safe.Call, 0, len(req.SignedBatch.BatchedCall.Calls))
	for _, call := range req.SignedBatch.BatchedCall.Calls {
		value, ok := new(big.Int).SetString(call.Value, 10)
		if !ok {
			return true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: "invalid call value",
			})
		}
		data := common.FromHex(call.Data)
		calls = append(calls, safe.Call{To: common.HexToAddress(call.To), Value: value, Data: data})
	}

	if a.safePolicy == nil {
		for _, call := range calls {
			if safe.IsExecTransaction(call.Data) {
				return true, handleForbiddenError(c, "Safe execution is not enabled")
			}
		}
		return false, nil
	}

	if _, err := a.safePolicy.Check(c.Request().Context(), calls); err != nil {
		if errors.Is(err, safe.ErrRejected) {
			return true, handleForbiddenError(c, err.Error())
		}
		a.logg.Error("safe policy check failed", "err", err)
		return true, c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeInternalServerError, Description: "could not verify Safe transaction",
		})
	}
	return false, nil
}

// callerMayManageRecord is canManageCatalogRecord extended with Safe signers:
// a caller who is not the recorded owner still qualifies when the owner is a
// multisig whose persisted signer projection contains them. Tracker events
// replace membership; ordinary metadata writes do not depend on RPC freshness.
//
// Off-chain catalog edits never pass the Safe's threshold, so any single signer
// of a multi-signer Safe can act alone here, including retirement.
func (a *API) callerMayManageRecord(ctx context.Context, tx pgx.Tx, callerAddress, callerRole string, ownerAddress *string) bool {
	if store.IsAdministrativeRole(callerRole) {
		return true // as canManageCatalogRecord, without a signer lookup
	}
	return canManageCatalogRecord(a.actingOwner(ctx, tx, callerAddress, ownerAddress), callerRole, ownerAddress)
}

// actingOwnerFor is actingOwner for the pure owner rules, which an administrator
// passes whatever address it is judged by: no signer lookup is needed.
func (a *API) actingOwnerFor(ctx context.Context, tx pgx.Tx, callerAddress, callerRole string, ownerAddress *string) string {
	if store.IsAdministrativeRole(callerRole) {
		return callerAddress
	}
	return a.actingOwner(ctx, tx, callerAddress, ownerAddress)
}

// actingOwner is the address to judge a caller by for an owner-only catalog
// write: the recorded owner when the caller is a projected signer of the owner
// Safe, otherwise the caller itself. It lets the pure owner rules
// (offeringSeller, canWriteVoucherOfferings) serve a Safe's signers unchanged,
// while anything recorded as the author still uses the real caller.
func (a *API) actingOwner(ctx context.Context, tx pgx.Tx, callerAddress string, ownerAddress *string) string {
	if callerAddress == "" || ownerAddress == nil || !common.IsHexAddress(*ownerAddress) ||
		strings.EqualFold(callerAddress, *ownerAddress) {
		return callerAddress
	}
	state, err := a.ensureSafeState(ctx, tx, *ownerAddress, false)
	if err != nil || state == nil {
		return callerAddress
	}
	for _, signer := range state.Signers {
		if strings.EqualFold(signer, callerAddress) {
			return *ownerAddress
		}
	}
	return callerAddress
}

func (a *API) queueSafeExecution(ctx context.Context, safeAddress, safeTxHash string) bool {
	if err := worker.EnqueueSafeExecution(ctx, a.queueClient, safeAddress, safeTxHash); err != nil {
		a.logg.Error("could not queue Safe execution", "safeTxHash", safeTxHash, "err", err)
		return false
	}
	return true
}

func (a *API) safeExecutionsQueued(ctx context.Context, safeTxHashes []string) map[string]bool {
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		a.logg.Error("could not read Safe executions", "err", err)
		return nil
	}
	defer tx.Rollback(ctx)
	queued, err := a.store.SafeExecutionsQueued(ctx, tx, safeTxHashes)
	if err != nil {
		a.logg.Error("could not read Safe executions", "err", err)
		return nil
	}
	return queued
}
