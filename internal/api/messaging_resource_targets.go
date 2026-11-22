package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// GET /messaging/resource-targets?kind=&resourceId= resolves the current
// recipients only when an authenticated user opens the composer. The same
// resource and owner projections also authorize contextual message writes.
func (a *API) messagingResourceTargetsHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	query := c.QueryParams()
	if len(query["kind"]) != 1 || len(query["resourceId"]) != 1 {
		return messagingRespond(c, messagingInvalid("kind and resourceId must each be supplied once"))
	}
	kind := c.QueryParam("kind")
	resourceID, err := parseMessagingResourceTargetID(kind, c.QueryParam("resourceId"))
	if err != nil {
		return messagingRespond(c, err)
	}

	ctx := c.Request().Context()
	canSeedSafe := a.safeReader != nil && (kind == "pool" || kind == "voucher")
	txOptions := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if canSeedSafe {
		txOptions.AccessMode = pgx.ReadWrite
	}
	tx, err := a.store.Pool().BeginTx(ctx, txOptions)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	caller, _ := messagingCaller(c)
	peers := []messagingPeer{}
	resource, err := a.store.MessagingResourceByID(ctx, tx, a.chainID, kind, resourceID)
	if err == nil {
		targets, err := a.store.ListOwnerMessageTargets(ctx, tx, a.chainID, resource.OwnerAddress)
		if err != nil {
			return err
		}
		// Existing Safe ownership reads seed legacy multisigs on first inspection.
		// Preserve that path for the composer without forcing a chain read for
		// an already resolved owner or a previously inspected address.
		if canSeedSafe && len(targets) == 0 && common.IsHexAddress(resource.OwnerAddress) && common.HexToAddress(resource.OwnerAddress) != (common.Address{}) {
			state, err := a.ensureSafeState(ctx, tx, resource.OwnerAddress, false)
			if err != nil {
				return err
			}
			if state != nil {
				targets, err = a.store.ListOwnerMessageTargets(ctx, tx, a.chainID, resource.OwnerAddress)
				if err != nil {
					return err
				}
			}
		}
		for _, target := range targets {
			if target.UserID != caller {
				peers = append(peers, messagingPeer{
					Address: checksum(target.Address), Username: target.Username, Avatar: target.Avatar,
				})
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"ok": true, "result": map[string]any{"targets": peers},
	})
}

func parseMessagingResourceTargetID(kind, id string) (string, error) {
	switch kind {
	case "pool", "voucher":
		address, err := messagingAddress(id, "resourceId")
		if err != nil {
			return "", err
		}
		return checksum(address), nil
	case "want", "catalog":
		n, err := strconv.ParseUint(id, 10, 63)
		if err != nil || n == 0 {
			return "", messagingInvalid("resourceId must be a positive integer")
		}
		return strconv.FormatUint(n, 10), nil
	default:
		return "", messagingInvalid("kind must be pool, voucher, want, or catalog")
	}
}
