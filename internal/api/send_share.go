package api

import (
	"bytes"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var (
	erc20TransferSelector = []byte{0xa9, 0x05, 0x9c, 0xbb}
)

func validateSendShare(share *apiresp.SendShareRequest) error {
	if share == nil {
		return nil
	}

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

func sendShareRecipient(batch apiresp.CaliburSignedBatch) (common.Address, error) {
	if len(batch.BatchedCall.Calls) != 1 {
		return common.Address{}, errors.New("a shared send must contain exactly one transfer call")
	}

	call := batch.BatchedCall.Calls[0]
	value, ok := new(big.Int).SetString(call.Value, 10)
	if !ok || value.Sign() != 0 {
		return common.Address{}, errors.New("a shared send cannot transfer native value")
	}

	data, err := hexutil.Decode(call.Data)
	if err != nil || len(data) != 4+32+32 || !bytes.Equal(data[:4], erc20TransferSelector) {
		return common.Address{}, errors.New("a shared send must be one ERC-20 transfer")
	}

	recipient := common.BytesToAddress(data[16:36])
	amount := new(big.Int).SetBytes(data[36:68])
	if recipient == (common.Address{}) || amount.Sign() <= 0 {
		return common.Address{}, errors.New("a shared send requires a recipient and positive amount")
	}

	return recipient, nil
}

func validateShareProfile(share apiresp.SendShareRequest, owner store.User) error {
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

func shareUnavailable(c *echo.Context, description string) error {
	return c.JSON(http.StatusUnprocessableEntity, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeShareUnavailable,
		Description: description,
	})
}

func userShareResult(share store.UserShareDetails) map[string]any {
	sharedBy := map[string]any{"name": share.OwnerName}
	if share.Email != "" {
		sharedBy["email"] = share.Email
	}
	if share.Phone != "" {
		sharedBy["phone"] = share.Phone
	}
	if share.LocationName != "" && share.GeoLat != nil && share.GeoLng != nil {
		sharedBy["location"] = map[string]any{
			"locationName": share.LocationName,
			"lat":          *share.GeoLat,
			"lng":          *share.GeoLng,
		}
	}

	return map[string]any{
		"id":         share.ID,
		"trackingId": share.TrackingID,
		"txHash":     share.TxHash,
		"permission": share.SharePermission,
		"note":       share.ExtraNote,
		"sharedBy":   sharedBy,
		"createdAt":  share.CreatedAt,
	}
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
