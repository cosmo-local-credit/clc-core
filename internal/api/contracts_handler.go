package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func (a *API) deployPoolHandler(c *echo.Context) error {
	req := apiresp.DeployPoolRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}

	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	if req.QuoterType == "" {
		req.QuoterType = "decimal"
	}

	if req.QuoterType == "oracle" {
		if req.BaseCurrency == "" {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: "Validation failed: baseCurrency is required when quoterType is oracle",
			})
		}
		if !common.IsHexAddress(req.BaseCurrency) {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: "Validation failed: baseCurrency must be a valid hex address",
			})
		}
		if common.HexToAddress(req.BaseCurrency) == ethutils.ZeroAddress {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: "Validation failed: baseCurrency must not be the zero address",
			})
		}
	}

	feePercent, feeParseErr := strconv.ParseFloat(req.FeePolicyDefault, 64)
	if feeParseErr != nil || feePercent < 0 || feePercent > 100 {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "feePolicyDefault must be a number between 0 and 100",
		})
	}

	callerType, _ := c.Get("callerType").(string)
	callerUserID, _ := c.Get("callerUserID").(uint64)

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if len(req.Tags) > 0 {
		validTags, err := a.store.ListTokenTags(ctx, tx)
		if err != nil {
			return handlePostgresError(c, err)
		}
		validTagSet := make(map[string]struct{}, len(validTags))
		for _, t := range validTags {
			validTagSet[t] = struct{}{}
		}
		for _, tag := range req.Tags {
			if _, ok := validTagSet[tag]; !ok {
				return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
					Ok:          false,
					ErrCode:     apiresp.ErrCodeValidationFailed,
					Description: fmt.Sprintf("invalid tag: %q", tag),
				})
			}
		}
	}

	// Quota check: applies to user callers only; superadmins and service tokens are exempt.
	if callerType == "user" {
		callerRole, _ := c.Get("callerRole").(string)
		if callerRole != "SUPERADMIN" {
			user, err := a.store.GetUserByID(ctx, tx, callerUserID)
			if err != nil {
				return handlePostgresError(c, err)
			}
			count, err := a.store.CountUserPools(ctx, tx, callerUserID)
			if err != nil {
				return err
			}
			if count >= user.MaxPools {
				return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
					Ok:          false,
					ErrCode:     apiresp.ErrCodeLimitExceeded,
					Description: fmt.Sprintf("pool creation limit of %d reached", user.MaxPools),
				})
			}
		}
	}

	swapPoolPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "SWAPPOOL")
	if err != nil {
		return handlePostgresError(c, err)
	}

	tokenIndexPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "TOKEN_UNIQUE_SYMBOL_INDEX")
	if err != nil {
		return handlePostgresError(c, err)
	}

	limiterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "LIMITER")
	if err != nil {
		return handlePostgresError(c, err)
	}

	feePolicyPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "FEE_POLICY")
	if err != nil {
		return handlePostgresError(c, err)
	}

	protocolFeeController, err := a.store.GetProtocolAddress(ctx, tx, "protocol_fee_controller")
	if err != nil {
		return handlePostgresError(c, err)
	}

	decimalQuoterAddr, err := a.store.GetProtocolAddress(ctx, tx, "decimal_quoter")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	args := worker.DeployPoolArgs{
		TrackingID:                 trackingID,
		PoolName:                   req.Name,
		PoolSymbol:                 req.Symbol,
		PoolDecimals:               req.Decimals,
		Owner:                      req.Owner,
		QuoterType:                 req.QuoterType,
		FeesDecoupled:              req.FeesDecoupled,
		FeePolicyDefaultFee:        req.FeePolicyDefault,
		BaseCurrency:               req.BaseCurrency,
		FactoryAddress:             swapPoolPC.FactoryAddress,
		SwapPoolImplAddress:        swapPoolPC.ImplementationAddress,
		TokenIndexImplAddress:      tokenIndexPC.ImplementationAddress,
		LimiterImplAddress:         limiterPC.ImplementationAddress,
		FeePolicyImplAddress:       feePolicyPC.ImplementationAddress,
		DecimalQuoterAddress:       decimalQuoterAddr,
		ProtocolFeeController:      protocolFeeController,
		SwapPoolPlatformContractID: swapPoolPC.ID,
		Description:                req.Description,
		Website:                    req.Website,
		Email:                      req.Email,
		Phone:                      req.Phone,
		LocationName:               req.LocationName,
		GeoLat:                     req.GeoLat,
		GeoLng:                     req.GeoLng,
		ImageURL:                   req.ImageURL,
		BannerURL:                  req.BannerURL,
		Tags:                       req.Tags,
	}

	switch req.QuoterType {
	case "oracle":
		quoterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "ORACLE_QUOTER")
		if err != nil {
			return handlePostgresError(c, err)
		}
		args.QuoterImplAddress = quoterPC.ImplementationAddress
	case "relative":
		quoterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "RELATIVE_QUOTER")
		if err != nil {
			return handlePostgresError(c, err)
		}
		args.QuoterImplAddress = quoterPC.ImplementationAddress
	}

	_, err = a.queueClient.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	// Record the deployment against the user so they can track it and so quota is enforced.
	if callerType == "user" {
		if err := a.store.InsertUserDeployment(ctx, tx, store.UserDeployment{
			UserID:       callerUserID,
			TrackingID:   trackingID,
			ResourceType: "POOL",
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pool deploy request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) deployTokenHandler(c *echo.Context) error {
	req := apiresp.DeployTokenRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}

	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	callerType, _ := c.Get("callerType").(string)
	callerUserID, _ := c.Get("callerUserID").(uint64)

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if len(req.Tags) > 0 {
		validTags, err := a.store.ListTokenTags(ctx, tx)
		if err != nil {
			return handlePostgresError(c, err)
		}
		validTagSet := make(map[string]struct{}, len(validTags))
		for _, t := range validTags {
			validTagSet[t] = struct{}{}
		}
		for _, tag := range req.Tags {
			if _, ok := validTagSet[tag]; !ok {
				return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
					Ok:          false,
					ErrCode:     apiresp.ErrCodeValidationFailed,
					Description: fmt.Sprintf("invalid tag: %q", tag),
				})
			}
		}
	}

	// Quota check: applies to user callers only; superadmins and service tokens are exempt.
	if callerType == "user" {
		callerRole, _ := c.Get("callerRole").(string)
		if callerRole != "SUPERADMIN" {
			user, err := a.store.GetUserByID(ctx, tx, callerUserID)
			if err != nil {
				return handlePostgresError(c, err)
			}
			count, err := a.store.CountUserTokens(ctx, tx, callerUserID)
			if err != nil {
				return err
			}
			if count >= user.MaxTokens {
				return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
					Ok:          false,
					ErrCode:     apiresp.ErrCodeLimitExceeded,
					Description: fmt.Sprintf("token creation limit of %d reached", user.MaxTokens),
				})
			}
		}
	}

	pc, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "ERC20")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(ctx, tx, worker.DeployTokenArgs{
		TrackingID:            trackingID,
		Name:                  req.Name,
		Symbol:                req.Symbol,
		Decimals:              req.Decimals,
		Owner:                 req.Owner,
		ExpiryTimestamp:       req.ExpiryTimestamp,
		FactoryAddress:        pc.FactoryAddress,
		ImplementationAddress: pc.ImplementationAddress,
		PlatformContractID:    pc.ID,
		Description:           req.Description,
		Website:               req.Website,
		Email:                 req.Email,
		Phone:                 req.Phone,
		VoucherUoa:            req.VoucherUoa,
		VoucherValue:          req.VoucherValue,
		LocationName:          req.LocationName,
		GeoLat:                req.GeoLat,
		GeoLng:                req.GeoLng,
		Tags:                  req.Tags,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	// Record the deployment against the user so they can track it and so quota is enforced.
	if callerType == "user" {
		if err := a.store.InsertUserDeployment(ctx, tx, store.UserDeployment{
			UserID:       callerUserID,
			TrackingID:   trackingID,
			ResourceType: "TOKEN",
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Token deploy request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) tokenTagsHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tags, err := a.store.ListTokenTags(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Available token tags",
		Result: map[string]any{
			"tags": tags,
		},
	})
}
