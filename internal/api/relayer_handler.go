package api

import (
	"errors"
	"math/big"
	"net/http"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func (a *API) relayerHealthHandler(c *echo.Context) error {
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relayer status",
		Result: map[string]any{
			"status":         "healthy",
			"build":          a.build,
			"chainId":        a.chainID,
			"relayerAddress": a.relayer.Address().Hex(),
			"caliburAddress": a.relayer.CaliburAddress().Hex(),
		},
	})
}

func (a *API) relayerEOANonceHandler(c *echo.Context) error {
	req := apiresp.RelayerEOANonceParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	nonce, err := a.relayer.EOANonce(c.Request().Context(), common.HexToAddress(req.Address))
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "EOA nonce",
		Result: map[string]any{
			"address": req.Address,
			"nonce":   strconv.FormatUint(nonce, 10),
		},
	})
}

func (a *API) caliburNonceHandler(c *echo.Context) error {
	req := apiresp.CaliburNonceParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	var keyHash common.Hash
	if req.KeyHash != "" {
		keyHash = common.HexToHash(req.KeyHash)
	}

	seq, err := a.relayer.CaliburSeq(c.Request().Context(), common.HexToAddress(req.Address), keyHash)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Calibur sequence",
		Result: map[string]any{
			"address": req.Address,
			"keyHash": keyHash.Hex(),
			"nonce":   strconv.FormatUint(seq, 10),
		},
	})
}

func (a *API) delegateHandler(c *echo.Context) error {
	req := apiresp.DelegateRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()
	ephemeralAddr := common.HexToAddress(req.EphemeralAddress)

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	nonce, err := a.relayer.AcquireNonce(ctx, tx)
	if err != nil {
		return err
	}

	signedTx, err := a.relayer.BuildAndSignDelegate(ctx, ephemeralAddr, req.Authorization, nonce)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	trackingID := uuid.NewString()
	prepared, err := a.relayer.InsertTrackedTx(ctx, tx, trackingID, store.RELAY_DELEGATE, nonce, signedTx)
	if err != nil {
		return err
	}

	if _, err := a.queueClient.InsertTx(ctx, tx, worker.DispatchArgs{
		TrackingID: trackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	a.logg.Info("relay delegate accepted",
		"trackingId", trackingID,
		"ephemeralAddress", ephemeralAddr.Hex(),
		"txHash", prepared.Hash,
	)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Delegate submitted",
		Result: map[string]any{
			"trackingId": trackingID,
			"txHash":     prepared.Hash,
		},
	})
}

func (a *API) relayHandler(c *echo.Context) error {
	req := apiresp.RelayRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()
	userAddr := common.HexToAddress(req.UserAddress)

	if common.HexToAddress(req.SignedBatch.Executor) != a.relayer.Address() {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeRelayerExecutorMismatch,
			Description: "signedBatch.executor must equal the relayer address",
		})
	}

	signature := common.FromHex(req.Signature)
	if len(signature) == 0 {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid signature hex",
		})
	}

	isDelegated, err := a.relayer.IsDelegated(ctx, userAddr)
	if err != nil {
		return err
	}
	if !isDelegated && req.Authorization == nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeAuthorizationRequired,
			Description: "user not delegated; include signed authorization",
		})
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	nonce, err := a.relayer.AcquireNonce(ctx, tx)
	if err != nil {
		return err
	}

	result, err := a.relayer.BuildAndSignRelay(
		ctx,
		userAddr,
		req.SignedBatch,
		signature,
		req.Authorization,
		isDelegated,
		nonce,
	)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	trackingID := uuid.NewString()
	prepared, err := a.relayer.InsertTrackedTx(ctx, tx, trackingID, result.OTXType, nonce, result.Tx)
	if err != nil {
		return err
	}

	if _, err := a.queueClient.InsertTx(ctx, tx, worker.DispatchArgs{
		TrackingID: trackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
		return err
	}

	// Persist the WebAuthn credential mapping when this relay carries a
	// registration payload (registration is signed by the ephemeral
	// secp256k1 key while the WebAuthn passkey itself doesn't exist yet
	// in our records).
	if req.SignatureType == "secp256k1" && req.CredentialID != "" && req.PublicKeyX != "" && req.PublicKeyY != "" {
		x, okX := new(big.Int).SetString(req.PublicKeyX, 10)
		y, okY := new(big.Int).SetString(req.PublicKeyY, 10)
		if !okX || !okY {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: "publicKeyX / publicKeyY must be decimal integers",
			})
		}
		keyHash := relayer.ComputeP256KeyHash(x, y)
		if err := a.store.UpsertWebAuthnCredential(ctx, tx, store.WebAuthnCredential{
			CredentialID: req.CredentialID,
			Address:      userAddr.Hex(),
			KeyHash:      keyHash.Hex(),
			PublicKeyX:   req.PublicKeyX,
			PublicKeyY:   req.PublicKeyY,
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	a.logg.Info("relay accepted",
		"trackingId", trackingID,
		"userAddress", userAddr.Hex(),
		"otxType", result.OTXType,
		"isDelegated", isDelegated,
		"txHash", prepared.Hash,
	)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relay submitted",
		Result: map[string]any{
			"trackingId": trackingID,
			"txHash":     prepared.Hash,
		},
	})
}

func (a *API) webAuthnChallengeHandler(c *echo.Context) error {
	challenge, err := a.relayer.IssueChallenge(c.Request().Context())
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "WebAuthn challenge",
		Result: map[string]any{
			"challenge": challenge,
		},
	})
}

func (a *API) webAuthnRecoverHandler(c *echo.Context) error {
	req := apiresp.WebAuthnRecoverRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	cred, err := a.store.GetWebAuthnCredential(ctx, tx, req.CredentialID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrNoRecordFound,
				Description: "credential not found",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	if err := a.relayer.VerifyAssertion(ctx, relayer.VerifyAssertionArgs{
		AuthenticatorData: req.AuthenticatorData,
		ClientDataJSON:    req.ClientDataJSON,
		Signature:         req.Signature,
		PublicKeyX:        cred.PublicKeyX,
		PublicKeyY:        cred.PublicKeyY,
	}); err != nil {
		a.logg.Warn("webauthn recover verification failed",
			"credentialId", req.CredentialID,
			"err", err,
		)
		return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeWebAuthnVerificationFailed,
			Description: "WebAuthn verification failed",
		})
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Wallet recovered",
		Result: map[string]any{
			"address":    cred.Address,
			"keyHash":    cred.KeyHash,
			"publicKeyX": cred.PublicKeyX,
			"publicKeyY": cred.PublicKeyY,
		},
	})
}
