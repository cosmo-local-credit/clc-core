package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/lmittmann/w3/module/eth"
)

var (
	erc20TransferTopic                 = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	errSendShareTransactionUnavailable = errors.New("send transaction is not confirmed")
)

func validateSendShare(share *apiresp.CreateSendShareRequest) error {
	share.Note = strings.TrimSpace(share.Note)
	if share.Permission < 0 || share.Permission&^store.SharePermissionAll != 0 {
		return errors.New("share contains an unknown permission")
	}
	if share.Permission == 0 && share.Note == "" {
		return errors.New("share must contain a note or contact permission")
	}
	if utf8.RuneCountInString(share.Note) > 500 {
		return errors.New("share note must be 500 characters or fewer")
	}

	return nil
}

func confirmedSendRecipient(
	chainTx *types.Transaction,
	receipt *types.Receipt,
	txHash common.Hash,
	chainID int64,
	owner common.Address,
) (common.Address, error) {
	if chainTx == nil || receipt == nil || receipt.BlockNumber == nil || receipt.Status != types.ReceiptStatusSuccessful {
		return common.Address{}, errSendShareTransactionUnavailable
	}
	if chainTx.Hash() != txHash || receipt.TxHash != txHash {
		return common.Address{}, errSendShareTransactionUnavailable
	}

	belongsToOwner := false
	sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(chainID)), chainTx)
	if err == nil && sender == owner {
		belongsToOwner = true
	}
	if to := chainTx.To(); to != nil && *to == owner {
		// EIP-7702 and other smart-account sends are submitted to the user's
		// account by a relayer, so the outer signer is intentionally different.
		belongsToOwner = true
	}
	if !belongsToOwner {
		return common.Address{}, errSendShareTransactionUnavailable
	}

	var (
		recipient     common.Address
		transferCount int
	)
	for _, eventLog := range receipt.Logs {
		if eventLog == nil || eventLog.Removed || len(eventLog.Topics) != 3 ||
			eventLog.Topics[0] != erc20TransferTopic || len(eventLog.Data) != 32 {
			continue
		}
		if common.BytesToAddress(eventLog.Topics[1].Bytes()) != owner {
			continue
		}
		if new(big.Int).SetBytes(eventLog.Data).Sign() <= 0 {
			continue
		}

		recipient = common.BytesToAddress(eventLog.Topics[2].Bytes())
		transferCount++
	}
	if transferCount != 1 || recipient == (common.Address{}) {
		return common.Address{}, errSendShareTransactionUnavailable
	}
	return recipient, nil
}

func (a *API) loadConfirmedSendRecipient(
	ctx context.Context,
	txHash common.Hash,
	owner common.Address,
) (common.Address, error) {
	if a.chainProvider == nil || a.chainProvider.Client == nil {
		return common.Address{}, errors.New("chain provider is unavailable")
	}

	var (
		chainTx *types.Transaction
		receipt *types.Receipt
	)
	if err := a.chainProvider.Client.CallCtx(ctx,
		eth.Tx(txHash).Returns(&chainTx),
		eth.TxReceipt(txHash).Returns(&receipt),
	); err != nil {
		return common.Address{}, fmt.Errorf("load confirmed send transaction: %w", err)
	}

	return confirmedSendRecipient(chainTx, receipt, txHash, a.chainID, owner)
}

func validateShareProfile(share apiresp.CreateSendShareRequest, owner store.User) error {
	if share.Permission&store.SharePermissionEmail != 0 &&
		(owner.Email == "" || strings.HasSuffix(strings.ToLower(owner.Email), "@clc.local")) {
		return errors.New("email is not available in the sender profile")
	}
	if share.Permission&store.SharePermissionPhone != 0 && owner.Phone == "" {
		return errors.New("phone is not available in the sender profile")
	}
	if share.Permission&store.SharePermissionLocation != 0 &&
		(owner.LocationName == nil || strings.TrimSpace(*owner.LocationName) == "" || owner.GeoLat == nil || owner.GeoLng == nil) {
		return errors.New("location is not available in the sender profile")
	}
	return nil
}

func matchesExistingShare(
	share store.UserShareDetails,
	ownerUserID uint64,
	authorizedUserID uint64,
	req apiresp.CreateSendShareRequest,
) bool {
	return share.OwnerUserID == ownerUserID &&
		share.AuthorizedUserID == authorizedUserID &&
		share.SharePermission == req.Permission &&
		share.ExtraNote == req.Note
}

func shareUnavailable(c *echo.Context, description string) error {
	return c.JSON(http.StatusUnprocessableEntity, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeShareUnavailable,
		Description: description,
	})
}

func userShareResult(share store.UserShareDetails) map[string]any {
	sharedBy := map[string]any{"name": share.OwnerName}
	if share.SharePermission&store.SharePermissionEmail != 0 && share.Email != "" {
		sharedBy["email"] = share.Email
	}
	if share.SharePermission&store.SharePermissionPhone != 0 && share.Phone != "" {
		sharedBy["phone"] = share.Phone
	}
	if share.SharePermission&store.SharePermissionLocation != 0 &&
		share.LocationName != "" && share.GeoLat != nil && share.GeoLng != nil {
		sharedBy["location"] = map[string]any{
			"locationName": share.LocationName,
			"lat":          *share.GeoLat,
			"lng":          *share.GeoLng,
		}
	}

	result := map[string]any{
		"id":         share.ID,
		"txHash":     share.TxHash,
		"permission": share.SharePermission,
		"note":       share.ExtraNote,
		"sharedBy":   sharedBy,
		"createdAt":  share.CreatedAt,
	}
	return result
}

func (a *API) userShareCreateHandler(c *echo.Context) error {
	var req apiresp.CreateSendShareRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	if err := validateSendShare(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	callerUserID, callerAddress, _ := getUserClaims(c)
	txHash := common.HexToHash(req.TxHash)
	recipientAddress, err := a.loadConfirmedSendRecipient(
		c.Request().Context(),
		txHash,
		common.HexToAddress(callerAddress),
	)
	if err != nil {
		if errors.Is(err, errSendShareTransactionUnavailable) {
			return shareUnavailable(c, "transaction is not a confirmed token send from this user")
		}
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	owner, err := a.store.GetUserByID(ctx, tx, callerUserID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !strings.EqualFold(owner.EthereumAddress, callerAddress) {
		return handleForbiddenError(c, "authenticated user does not own the confirmed transaction")
	}

	recipient, err := a.store.GetUserByAddress(ctx, tx, recipientAddress.Hex())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return shareUnavailable(c, "recipient is not available for private sharing")
		}
		return handlePostgresError(c, err)
	}
	existing, err := a.store.GetUserShareByTxHash(ctx, tx, txHash.Hex(), owner.ID)
	if err == nil {
		if !matchesExistingShare(existing, owner.ID, recipient.ID, req) {
			return c.JSON(http.StatusConflict, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeConflict,
				Description: "a different private attachment already exists for this transaction",
			})
		}
		if err := tx.Commit(ctx); err != nil {
			return handlePostgresError(c, err)
		}
		return c.JSON(http.StatusOK, apiresp.OKResponse{
			Ok:          true,
			Description: "Private send attachment saved",
			Result: map[string]any{
				"shareId": existing.ID,
				"txHash":  txHash.Hex(),
			},
		})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}
	if err := validateShareProfile(req, owner); err != nil {
		return shareUnavailable(c, err.Error())
	}
	if recipient.Status != "APPROVED" {
		return shareUnavailable(c, "recipient is not available for private sharing")
	}
	if recipient.ID == owner.ID {
		return shareUnavailable(c, "private attachments are not supported for self-sends")
	}

	shareID, err := a.store.InsertUserShare(ctx, tx, store.UserShare{
		TxHash:           txHash.Hex(),
		OwnerUserID:      owner.ID,
		AuthorizedUserID: recipient.ID,
		SharePermission:  req.Permission,
		ExtraNote:        req.Note,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusConflict, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeConflict,
				Description: "a different private attachment already exists for this transaction",
			})
		}
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	if a.logg != nil {
		a.logg.Info("private send attachment saved",
			"share_id", shareID,
			"tx_hash", txHash.Hex(),
			"owner_user_id", owner.ID,
			"authorized_user_id", recipient.ID,
			"permission", req.Permission,
			"status", "CONFIRMED",
		)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Private send attachment saved",
		Result: map[string]any{
			"shareId": shareID,
			"txHash":  txHash.Hex(),
		},
	})
}

func (a *API) userShareByTxHashHandler(c *echo.Context) error {
	var req apiresp.TxHashParam
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	callerUserID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	share, err := a.store.GetUserShareByTxHash(ctx, tx, req.TxHash, callerUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "share not found",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	if a.logg != nil {
		a.logg.Info("private send attachment read",
			"share_id", share.ID,
			"caller_user_id", callerUserID,
			"owner_user_id", share.OwnerUserID,
			"authorized_user_id", share.AuthorizedUserID,
			"permission", share.SharePermission,
			"status", "SUCCESS",
		)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Private send attachment",
		Result:      userShareResult(share),
	})
}

func (a *API) userShareEligibilityHandler(c *echo.Context) error {
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid recipient address",
		})
	}

	callerUserID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	eligible := false
	recipient, err := a.store.GetUserByAddress(ctx, tx, common.HexToAddress(address).Hex())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}
	if err == nil {
		eligible = recipient.Status == "APPROVED" && recipient.ID != callerUserID
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Private share eligibility",
		Result: map[string]any{
			"eligible": eligible,
		},
	})
}
