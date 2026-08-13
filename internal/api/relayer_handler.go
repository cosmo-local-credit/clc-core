package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func (a *API) relayerHealthHandler(c *echo.Context) error {
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relayer status",
		Result: map[string]any{
			"status":         "healthy",
			"build":          a.build,
			"chainId":        a.chainID,
			"relayerAddress": a.relayer.SignerAddress().Hex(),
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

func isPublicRelayerTrackingChain(otxs []*store.OTX) bool {
	for _, otx := range otxs {
		switch otx.OTXType {
		case store.RELAY_DELEGATE, store.RELAY_EXEC, store.RELAY_DELEGATE_EXEC:
		default:
			return false
		}
	}
	return true
}

// relayerTrackingStatus decides the HTTP outcome for a tracking lookup.
// An empty result means the OTX row hasn't been written by the async
// relayer worker yet
func relayerTrackingStatus(otxs []*store.OTX) (status int, errCode, description string) {
	if len(otxs) == 0 {
		return http.StatusNotFound, apiresp.ErrCodeNotFound, "tracking id not found"
	}
	if !isPublicRelayerTrackingChain(otxs) {
		return http.StatusForbidden, apiresp.ErrCodeForbidden, "tracking id is not public relayer flow"
	}
	return http.StatusOK, "", ""
}

func relayDelegateTrackingID(result *rivertype.JobInsertResult, fallback string) string {
	if result == nil || result.Job == nil || len(result.Job.EncodedArgs) == 0 {
		return fallback
	}

	var args worker.RelayDelegateArgs
	if err := json.Unmarshal(result.Job.EncodedArgs, &args); err != nil || args.TrackingID == "" {
		return fallback
	}
	return args.TrackingID
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

// delegateHandler is a thin job-queue client: it validates the request and
// hands it to the RELAY_DELEGATE river worker, which loads the master key,
// acquires the nonce, signs, and dispatches within its own transaction. The
// returned trackingId lets the caller follow the job.
func (a *API) delegateHandler(c *echo.Context) error {
	req := apiresp.DelegateRequest{}
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

	trackingID := uuid.NewString()
	registerTrackingID := uuid.NewString()

	insertResult, err := a.queueClient.InsertTx(ctx, tx, worker.RelayDelegateArgs{
		TrackingID:       trackingID,
		EphemeralAddress: req.EphemeralAddress,
		Authorization:    req.Authorization,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	duplicate := insertResult != nil && insertResult.UniqueSkippedAsDuplicate
	if duplicate {
		trackingID = relayDelegateTrackingID(insertResult, trackingID)
		registerTrackingID = ""
	} else if _, err := a.queueClient.InsertTx(ctx, tx, worker.RegisterUserArgs{
		TrackingID:  registerTrackingID,
		UserAddress: req.EphemeralAddress,
	}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Delegate request accepted",
		Result: map[string]any{
			"trackingId":         trackingID,
			"registerTrackingId": registerTrackingID,
			"duplicate":          duplicate,
		},
	})
}

// relayHandler queues a relay and, for an authenticated share-bearing send,
// persists its private attachment in the same database transaction.
func (a *API) relayHandler(c *echo.Context) error {
	req := apiresp.RelayRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	if err := validateSendShare(req.Share); err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	var (
		callerUserID     uint64
		recipientAddress common.Address
	)
	if req.Share != nil {
		var callerAddress string
		callerUserID, callerAddress, _ = getUserClaims(c)
		if callerUserID == 0 {
			return handleJWTAuthError(c, "user authentication is required to attach a share")
		}
		if !strings.EqualFold(callerAddress, req.UserAddress) {
			return handleForbiddenError(c, "authenticated user does not own the relay address")
		}

		var err error
		recipientAddress, err = sendShareRecipient(req.SignedBatch)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: err.Error(),
			})
		}
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	trackingID := uuid.NewString()
	var (
		shareID          uint64
		authorizedUserID uint64
	)

	if req.Share != nil {
		owner, err := a.store.GetUserByID(ctx, tx, callerUserID)
		if err != nil {
			return handlePostgresError(c, err)
		}
		if !strings.EqualFold(owner.EthereumAddress, req.UserAddress) {
			return handleForbiddenError(c, "authenticated user does not own the relay address")
		}
		if err := validateShareProfile(*req.Share, owner); err != nil {
			return shareUnavailable(c, err.Error())
		}

		recipient, err := a.store.GetUserByAddress(ctx, tx, recipientAddress.Hex())
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return shareUnavailable(c, "recipient is not available for private sharing")
			}
			return handlePostgresError(c, err)
		}
		if recipient.Status != "APPROVED" {
			return shareUnavailable(c, "recipient is not available for private sharing")
		}
		if recipient.ID == owner.ID {
			return shareUnavailable(c, "private attachments are not supported for self-sends")
		}
		authorizedUserID = recipient.ID

		shareID, err = a.store.InsertUserShare(ctx, tx, store.UserShare{
			TrackingID:       trackingID,
			OwnerUserID:      owner.ID,
			AuthorizedUserID: recipient.ID,
			SharePermission:  req.Share.Permission,
			ExtraNote:        req.Share.Note,
		})
		if err != nil {
			return handlePostgresError(c, err)
		}
	}

	if _, err := a.queueClient.InsertTx(ctx, tx, worker.RelayArgs{
		TrackingID:    trackingID,
		UserAddress:   req.UserAddress,
		SignedBatch:   req.SignedBatch,
		Signature:     req.Signature,
		SignatureType: req.SignatureType,
		Authorization: req.Authorization,
		CredentialID:  req.CredentialID,
		PublicKeyX:    req.PublicKeyX,
		PublicKeyY:    req.PublicKeyY,
	}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	result := map[string]any{"trackingId": trackingID}
	if shareID != 0 {
		result["shareId"] = shareID
		if a.logg != nil {
			a.logg.Info("private send attachment queued",
				"share_id", shareID,
				"tracking_id", trackingID,
				"owner_user_id", callerUserID,
				"authorized_user_id", authorizedUserID,
				"permission", req.Share.Permission,
				"status", "QUEUED",
			)
		}
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relay request accepted",
		Result:      result,
	})
}

// relayPreviewHandler resolves the Calibur nonce for the given (user, keyHash)
// and returns the EIP-712 digest the client must sign over the batch. It is a
// read-only convenience: the client may instead derive the digest itself, but
// using this guarantees the signed value matches what the relayer submits.
func (a *API) relayPreviewHandler(c *echo.Context) error {
	req := apiresp.RelayPreviewRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	nonce, digest, err := a.relayer.PreviewBatch(
		c.Request().Context(),
		common.HexToAddress(req.UserAddress),
		req.BatchedCall,
		req.KeyHash,
		req.Executor,
		req.Deadline,
	)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relay batch preview",
		Result: map[string]any{
			"nonce":  nonce,
			"digest": digest,
		},
	})
}

func (a *API) trackRelayerOTXHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	req := apiresp.TrackOTXRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	otxs, err := a.store.GetOTXByTrackingID(ctx, tx, req.TrackingID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	if status, errCode, description := relayerTrackingStatus(otxs); status != http.StatusOK {
		return c.JSON(status, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     errCode,
			Description: description,
		})
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relayer OTX chain",
		Result: map[string]any{
			"otx": otxs,
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
