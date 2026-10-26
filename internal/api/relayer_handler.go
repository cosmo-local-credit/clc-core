package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

var (
	erc20TransferSelector         = functionSelector("transfer(address,uint256)")
	erc20ApproveSelector          = functionSelector("approve(address,uint256)")
	poolWithdrawSelector          = functionSelector("withdraw(address,address,uint256)")
	poolWithdrawRecipientSelector = functionSelector("withdraw(address,address,uint256,address)")
)

func functionSelector(signature string) []byte {
	return crypto.Keccak256([]byte(signature))[:4]
}

// directVoucherTransfer recognizes the exact one-call ERC-20 transfer shape
// used by Send and Redeem. Only the voucher identity is needed: the immutable
// claim is snapshotted now, while Activity later supplies the confirmed amount.
func directVoucherTransfer(batch apiresp.CaliburSignedBatch) (tokenAddress string, ok bool) {
	if len(batch.BatchedCall.Calls) != 1 {
		return "", false
	}
	call := batch.BatchedCall.Calls[0]
	if !zeroCallValue(call) || !common.IsHexAddress(call.To) {
		return "", false
	}
	data, err := hexutil.Decode(call.Data)
	if err != nil || len(data) != 68 || !bytes.Equal(data[:4], erc20TransferSelector) {
		return "", false
	}
	recipient, valid := calldataAddress(data, 0)
	if !valid || recipient == (common.Address{}) {
		return "", false
	}
	amount := calldataUint(data, 1)
	if amount.Sign() <= 0 {
		return "", false
	}
	token := common.HexToAddress(call.To)
	if token == (common.Address{}) {
		return "", false
	}
	return token.Hex(), true
}

// valuationVoucherAddresses extracts only the exact operations this client
// submits for Send/Redeem or pool swaps. It performs no RPC or contract read;
// every address comes from the already-signed calldata.
func valuationVoucherAddresses(batch apiresp.CaliburSignedBatch) []string {
	if token, ok := directVoucherTransfer(batch); ok {
		return []string{token}
	}
	return poolSwapVoucherAddresses(batch)
}

// poolSwapVoucherAddresses validates the app's three-call-per-hop structure:
// reset approval, exact approval, then SwapPool.withdraw. Both sides of every
// hop are returned so their database claims are preserved before execution.
func poolSwapVoucherAddresses(batch apiresp.CaliburSignedBatch) []string {
	calls := batch.BatchedCall.Calls
	if len(calls) == 0 || len(calls)%3 != 0 {
		return nil
	}

	seen := make(map[common.Address]struct{})
	addresses := make([]string, 0, len(calls)/3+1)
	appendAddress := func(address common.Address) {
		if _, exists := seen[address]; exists {
			return
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address.Hex())
	}

	for i := 0; i < len(calls); i += 3 {
		resetToken, resetSpender, resetAmount, ok := decodeApproval(calls[i])
		if !ok || resetAmount.Sign() != 0 {
			return nil
		}
		approvedToken, approvedSpender, approvedAmount, ok := decodeApproval(calls[i+1])
		if !ok || approvedAmount.Sign() <= 0 {
			return nil
		}
		pool, tokenOut, tokenIn, withdrawnAmount, ok := decodePoolWithdraw(calls[i+2])
		if !ok || tokenIn == tokenOut || resetToken != approvedToken || resetToken != tokenIn ||
			resetSpender != approvedSpender || resetSpender != pool || approvedAmount.Cmp(withdrawnAmount) != 0 {
			return nil
		}
		appendAddress(tokenIn)
		appendAddress(tokenOut)
	}
	return addresses
}

func decodeApproval(call apiresp.CaliburCall) (token, spender common.Address, amount *big.Int, ok bool) {
	if !zeroCallValue(call) || !common.IsHexAddress(call.To) {
		return common.Address{}, common.Address{}, nil, false
	}
	data, err := hexutil.Decode(call.Data)
	if err != nil || len(data) != 68 || !bytes.Equal(data[:4], erc20ApproveSelector) {
		return common.Address{}, common.Address{}, nil, false
	}
	spender, ok = calldataAddress(data, 0)
	token = common.HexToAddress(call.To)
	if !ok || token == (common.Address{}) || spender == (common.Address{}) {
		return common.Address{}, common.Address{}, nil, false
	}
	return token, spender, calldataUint(data, 1), true
}

func decodePoolWithdraw(call apiresp.CaliburCall) (pool, tokenOut, tokenIn common.Address, amount *big.Int, ok bool) {
	if !zeroCallValue(call) || !common.IsHexAddress(call.To) {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	data, err := hexutil.Decode(call.Data)
	if err != nil {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	hasRecipient := len(data) == 132 && bytes.Equal(data[:4], poolWithdrawRecipientSelector)
	if (len(data) != 100 || !bytes.Equal(data[:4], poolWithdrawSelector)) && !hasRecipient {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	tokenOut, ok = calldataAddress(data, 0)
	if !ok {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	tokenIn, ok = calldataAddress(data, 1)
	if !ok {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	if hasRecipient {
		recipient, valid := calldataAddress(data, 3)
		if !valid || recipient == (common.Address{}) {
			return common.Address{}, common.Address{}, common.Address{}, nil, false
		}
	}
	pool = common.HexToAddress(call.To)
	amount = calldataUint(data, 2)
	if pool == (common.Address{}) || tokenOut == (common.Address{}) || tokenIn == (common.Address{}) || amount.Sign() <= 0 {
		return common.Address{}, common.Address{}, common.Address{}, nil, false
	}
	return pool, tokenOut, tokenIn, amount, true
}

func zeroCallValue(call apiresp.CaliburCall) bool {
	value, valid := new(big.Int).SetString(call.Value, 10)
	return valid && value.Sign() == 0
}

func calldataAddress(data []byte, index int) (common.Address, bool) {
	start := 4 + index*32
	if start < 4 || start+32 > len(data) {
		return common.Address{}, false
	}
	for _, b := range data[start : start+12] {
		if b != 0 {
			return common.Address{}, false
		}
	}
	return common.BytesToAddress(data[start+12 : start+32]), true
}

func calldataUint(data []byte, index int) *big.Int {
	start := 4 + index*32
	if start < 4 || start+32 > len(data) {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(data[start : start+32])
}

func (a *API) relayerHealthHandler(c *echo.Context) error {
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relayer status",
		Result: map[string]any{
			"status":         "healthy",
			"build":          a.build,
			"chainId":        a.chainID,
			"relayerAddress": a.relayer.SignerAddress().Hex(),
			// POST /relay honours operationId: a lost response is reconciled by
			// resending the same signed request, never by signing a new one.
			"idempotentRelay": true,
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

// relayHandler queues a relay job. Private send attachments are created only
// after confirmation through the authenticated user-share endpoint.
func (a *API) relayHandler(c *echo.Context) error {
	req := apiresp.RelayRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	ctx := c.Request().Context()

	args := worker.RelayArgs{
		UserAddress:   req.UserAddress,
		SignedBatch:   req.SignedBatch,
		Signature:     req.Signature,
		SignatureType: req.SignatureType,
		Authorization: req.Authorization,
		CredentialID:  req.CredentialID,
		PublicKeyX:    req.PublicKeyX,
		PublicKeyY:    req.PublicKeyY,
	}

	// A registration is rejected here, before a job exists, unless the wallet's
	// root key signed it and it registers exactly the supplied key. The route is
	// public, so this proof is the only thing standing between an anonymous
	// caller and a passkey mapped to someone else's address.
	if args.HasPartialRegistration() {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "credentialId, publicKeyX and publicKeyY must be sent together",
		})
	}
	var registrationKeyHash common.Hash
	if registration := args.Registration(); registration != nil {
		userAddr := common.HexToAddress(req.UserAddress)
		keyHash, err := a.relayer.VerifyRegistration(relayer.RegistrationProof{
			UserAddress:   userAddr,
			SignedBatch:   req.SignedBatch,
			Signature:     common.FromHex(req.Signature),
			SignatureType: req.SignatureType,
			PublicKeyX:    registration.PublicKeyX,
			PublicKeyY:    registration.PublicKeyY,
		})
		if err != nil {
			a.logg.Warn("relay registration rejected",
				"userAddress", req.UserAddress,
				"credentialId", registration.CredentialID,
				"err", err,
			)
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: err.Error(),
			})
		}

		registrationKeyHash = keyHash
	}

	var operationDigest []byte
	if req.OperationID != "" {
		digest, err := relayOperationDigest(a.chainID, req)
		if err != nil {
			return err
		}
		operationDigest = digest
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	trackingID := uuid.NewString()

	// Claimed before anything else in the transaction, so a resubmission is
	// answered from the first one's row rather than re-judged: a registration
	// the first submission made pending must not turn its own replay into a
	// conflict. The claim rolls back with the job if anything below fails.
	if operationDigest != nil {
		existing, claimed, err := a.store.ClaimRelayOperation(ctx, tx, store.RelayOperation{
			OperationID:   req.OperationID,
			UserAddress:   req.UserAddress,
			RequestDigest: operationDigest,
			TrackingID:    trackingID,
		})
		if err != nil {
			return handlePostgresError(c, err)
		}
		if !claimed {
			if !isRelayOperationReplay(existing, req.UserAddress, operationDigest) {
				return relayOperationConflict(c)
			}
			return relayOperationReplayed(c, req.OperationID, existing.TrackingID)
		}
	}

	if registration := args.Registration(); registration != nil {
		var existing *store.WebAuthnCredential
		row, err := a.store.GetWebAuthnCredential(ctx, tx, registration.CredentialID)
		switch {
		case err == nil:
			existing = &row
		case !errors.Is(err, pgx.ErrNoRows):
			return handlePostgresError(c, err)
		}
		if err := store.CheckWebAuthnRegistration(existing, store.WebAuthnCredential{
			Address: common.HexToAddress(req.UserAddress).Hex(),
			KeyHash: registrationKeyHash.Hex(),
		}); err != nil {
			return webAuthnRegistrationConflict(c, err)
		}
	}

	if err := a.store.CheckPoolOperationsAllowed(ctx, tx, a.chainID, poolOperationTargets(req.SignedBatch.BatchedCall)); err != nil {
		if errors.Is(err, store.ErrRetiredPool) {
			return handleConflictError(c, err.Error())
		}
		return handlePostgresError(c, err)
	}

	args.TrackingID = trackingID

	if _, err := a.queueClient.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: 1}); err != nil {
		return handlePostgresError(c, err)
	}
	valuationSnapshots := []store.VoucherClaimSnapshot{}
	if tokenAddresses := valuationVoucherAddresses(req.SignedBatch); len(tokenAddresses) > 0 {
		valuationSnapshots, err = a.store.InsertTransferValuationSnapshots(ctx, tx, trackingID, tokenAddresses)
		if err != nil {
			return handlePostgresError(c, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	result := map[string]any{
		"trackingId":         trackingID,
		"valuationSnapshots": valuationSnapshots,
	}
	if req.OperationID != "" {
		result["operationId"] = req.OperationID
		result["replayed"] = false
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relay request accepted",
		Result:      result,
	})
}

// webAuthnRegistrationConflict answers a registration whose credential id is
// already taken. A pending registration of the same mapping hands back its
// tracking id so the client resumes following it instead of relaying again.
func webAuthnRegistrationConflict(c *echo.Context, err error) error {
	var regErr *store.WebAuthnRegistrationError
	if !errors.As(err, &regErr) {
		return err
	}
	result := map[string]any{"state": regErr.Existing.State}
	if errors.Is(err, store.ErrWebAuthnRegistrationPending) && regErr.Existing.TrackingID != nil {
		result["trackingId"] = *regErr.Existing.TrackingID
	}
	return c.JSON(http.StatusConflict, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeConflict,
		Description: regErr.Error(),
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
	ctx := c.Request().Context()
	if targets := poolOperationTargets(req.BatchedCall); len(targets) > 0 {
		tx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err := a.store.CheckPoolOperationsAllowed(ctx, tx, a.chainID, targets); err != nil {
			if errors.Is(err, store.ErrRetiredPool) {
				return handleConflictError(c, err.Error())
			}
			return handlePostgresError(c, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return handlePostgresError(c, err)
		}
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

	cred, err := a.store.GetActiveWebAuthnCredential(ctx, tx, req.CredentialID)
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
