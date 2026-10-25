package api

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// safeSignerMaxAge bounds how stale a cached signer set may be and still
// authorize an off-chain write. The event feed refreshes it; past this age the
// set is treated as unknown, so a missed RemovedOwner cannot keep a removed
// signer in control.
const safeSignerMaxAge = 24 * time.Hour

// storeSafeResolver answers policy questions from the signer cache and the
// catalog. Every method reads in its own short transaction and returns an
// error on any failure, which Check treats as a rejection.
type storeSafeResolver struct{ a *API }

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
// Safe whose fresh cached signer set contains them. A missing or stale cache
// denies.
//
// Off-chain catalog edits never pass the Safe's threshold, so any single signer
// of a multi-signer Safe can act alone here; see SAFE.md, open question 1.
func (a *API) callerMayManageRecord(ctx context.Context, tx pgx.Tx, callerAddress, callerRole string, ownerAddress *string) bool {
	if canManageCatalogRecord(callerAddress, callerRole, ownerAddress) {
		return true
	}
	if callerAddress == "" || ownerAddress == nil || !common.IsHexAddress(*ownerAddress) {
		return false
	}
	state, err := a.store.GetSafeState(ctx, tx, a.chainID, *ownerAddress)
	if err != nil || time.Since(state.RefreshedAt) > safeSignerMaxAge {
		return false
	}
	for _, signer := range state.Signers {
		if strings.EqualFold(signer, callerAddress) {
			return true
		}
	}
	return false
}
