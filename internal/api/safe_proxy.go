package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var (
	safeTxHashPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

type (
	safeSTSClient interface {
		Propose(ctx context.Context, safe, safeTxHash, sender, signature string, f safe.TxFields) error
		Confirm(ctx context.Context, safeTxHash, signature string) error
		Transaction(ctx context.Context, safeTxHash string) (*safe.Transaction, error)
		Nonce(ctx context.Context, safe string) (string, error)
		Queued(ctx context.Context, safe, nonceGte string) ([]safe.Transaction, error)
	}

	// safeSignerSource returns a Safe's current signers. ok is false for an
	// address that is not a Safe or whose set cannot be trusted right now; the
	// proxy then denies.
	// safeStateReader reads a Safe at the chain head; safe.Reader satisfies it.
	safeStateReader interface {
		ReadSafeLatest(ctx context.Context, address common.Address) (*safe.State, uint64, error)
	}

	safeSignerSource interface {
		Signers(ctx context.Context, safe common.Address) (signers []string, ok bool, err error)
	}

	safeService struct {
		chainID int64
		policy  *safe.Policy
		sts     safeSTSClient
		signers safeSignerSource
		// execute queues clc-core's execution of a confirmed transaction and
		// executing reports which are queued; both nil leaves execution to
		// the Safe app.
		execute   func(ctx context.Context, safe, safeTxHash string) bool
		executing func(ctx context.Context, safeTxHashes []string) map[string]bool
		// contracts verifies contract (v 0) signatures against a Safe at the
		// latest block; nil rejects them.
		contracts func(safe common.Address) safe.ContractVerifier
	}

	safeProposalRequest struct {
		Safe       string        `json:"safe"`
		SafeTxHash string        `json:"safeTxHash"`
		Sender     string        `json:"sender"`
		Signature  string        `json:"signature"`
		Tx         safe.TxFields `json:"tx"`
	}

	safeConfirmationRequest struct {
		SafeTxHash string `json:"safeTxHash"`
		Signature  string `json:"signature"`
	}

	// proxyError carries the HTTP outcome of a rejected proxy call.
	proxyError struct {
		status  int
		code    string
		message string
	}

	// storeSafeSigners reads the persisted signer projection, discovering an
	// existing multisig if needed.
	storeSafeSigners struct{ a *API }
)

func (e *proxyError) Error() string { return e.message }

func badRequest(msg string) error {
	return &proxyError{http.StatusBadRequest, apiresp.ErrCodeValidationFailed, msg}
}

func forbidden(msg string) error {
	return &proxyError{http.StatusForbidden, apiresp.ErrCodeForbidden, msg}
}

// upstreamError maps a Transaction Service failure. A 4xx is the service
// refusing the content (a used nonce, a duplicate): the caller's conflict, not
// an outage. Everything else is a bad gateway.
func upstreamError(err error) error {
	var sErr *safe.STSError
	if errors.As(err, &sErr) {
		switch {
		case sErr.Status == http.StatusNotFound:
			return &proxyError{http.StatusNotFound, apiresp.ErrCodeNotFound, "Safe transaction not found"}
		case sErr.Status >= 400 && sErr.Status < 500:
			return &proxyError{http.StatusConflict, apiresp.ErrCodeConflict, "Safe Transaction Service refused the request: " + sErr.Message}
		}
	}
	return &proxyError{http.StatusBadGateway, apiresp.ErrCodeInternalServerError, "Safe Transaction Service unavailable"}
}

func policyError(err error) error {
	if errors.Is(err, safe.ErrRejected) {
		return forbidden(err.Error())
	}
	return &proxyError{http.StatusServiceUnavailable, apiresp.ErrCodeInternalServerError, "could not verify Safe transaction"}
}

func (s *safeService) requireSigner(ctx context.Context, caller string, safeAddress common.Address) error {
	signers, ok, err := s.signers.Signers(ctx, safeAddress)
	if err != nil {
		return &proxyError{http.StatusServiceUnavailable, apiresp.ErrCodeInternalServerError, "could not verify Safe signers"}
	}
	if ok {
		for _, signer := range signers {
			if strings.EqualFold(signer, caller) {
				return nil
			}
		}
	}
	return forbidden("only a signer of this Safe may do this")
}

// verifyTransaction is the checks a proposal and a confirmation share: the
// fields parse, the hash is the real EIP-712 digest, and policy accepts them.
func (s *safeService) verifyTransaction(ctx context.Context, safeAddress common.Address, claimedHash string, f safe.TxFields) (common.Hash, error) {
	exec, err := f.Exec()
	if err != nil {
		return common.Hash{}, badRequest(err.Error())
	}
	hash, err := safe.SafeTxHash(s.chainID, safeAddress, f)
	if err != nil {
		return common.Hash{}, badRequest(err.Error())
	}
	if !strings.EqualFold(hash.Hex(), claimedHash) {
		return common.Hash{}, badRequest("safeTxHash does not match the transaction")
	}
	if err := s.policy.CheckTransaction(ctx, safeAddress, exec); err != nil {
		return common.Hash{}, policyError(err)
	}
	return hash, nil
}

func recoverFrom(hash common.Hash, signature string) (common.Address, error) {
	raw, err := hexutil.Decode(signature)
	if err != nil {
		return common.Address{}, badRequest("invalid signature")
	}
	signer, err := safe.RecoverSigner(hash, raw)
	if err != nil {
		return common.Address{}, badRequest(err.Error())
	}
	return signer, nil
}

// signerOf returns the owner a signature speaks for. An ECDSA signature is
// recovered. A contract signature names its owner and is checked through that
// owner's isValidSignature, which a Safe calls that way only from 1.5.0 on.
func (s *safeService) signerOf(ctx context.Context, safeAddress common.Address, hash common.Hash, signature string) (common.Address, error) {
	raw, err := hexutil.Decode(signature)
	if err != nil {
		return common.Address{}, badRequest("invalid signature")
	}
	if s.contracts == nil || !safe.IsContractSignature(raw) {
		return recoverFrom(hash, signature)
	}
	parsed, err := safe.ParseContractSignature(raw)
	if err != nil {
		return common.Address{}, badRequest("invalid contract signature")
	}
	unavailable := &proxyError{http.StatusServiceUnavailable, apiresp.ErrCodeInternalServerError, "could not verify signature"}
	verifier := s.contracts(safeAddress)
	accepts, err := verifier.Accepts(ctx)
	if err != nil {
		return common.Address{}, unavailable
	}
	if !accepts {
		return common.Address{}, forbidden("this Safe accepts contract signatures from version 1.5.0")
	}
	ok, err := verifier.Valid(ctx, parsed.Owner, hash, parsed.Data)
	if err != nil {
		return common.Address{}, unavailable
	}
	if !ok {
		return common.Address{}, badRequest("contract signature is not valid for its owner")
	}
	return parsed.Owner, nil
}

func (s *safeService) propose(ctx context.Context, caller string, req safeProposalRequest) (*safe.Transaction, error) {
	if !common.IsHexAddress(req.Safe) || !common.IsHexAddress(req.Sender) || !safeTxHashPattern.MatchString(req.SafeTxHash) {
		return nil, badRequest("invalid safe, sender or safeTxHash")
	}
	if !strings.EqualFold(req.Sender, caller) {
		return nil, forbidden("sender must be the signed-in account")
	}
	safeAddress := common.HexToAddress(req.Safe)
	if err := s.requireSigner(ctx, caller, safeAddress); err != nil {
		return nil, err
	}
	hash, err := s.verifyTransaction(ctx, safeAddress, req.SafeTxHash, req.Tx)
	if err != nil {
		return nil, err
	}
	signer, err := s.signerOf(ctx, safeAddress, hash, req.Signature)
	if err != nil {
		return nil, err
	}
	if signer != common.HexToAddress(req.Sender) {
		return nil, badRequest("signature was not made by sender")
	}
	if err := s.sts.Propose(ctx, safeAddress.Hex(), hash.Hex(), signer.Hex(), req.Signature, req.Tx); err != nil {
		return nil, upstreamError(err)
	}
	tx, err := s.sts.Transaction(ctx, hash.Hex())
	if err != nil {
		return nil, upstreamError(err)
	}
	s.queueExecution(ctx, tx)
	return tx, nil
}

// queueExecution hands a transaction that has just reached its threshold to
// clc-core's executor. The caller's signature is recorded either way; a failed
// enqueue leaves execution to the Safe app.
func (s *safeService) queueExecution(ctx context.Context, tx *safe.Transaction) {
	if s.execute == nil || tx.IsExecuted || tx.ConfirmationsRequired < 1 ||
		len(tx.Confirmations) < tx.ConfirmationsRequired {
		return
	}
	tx.Executing = s.execute(ctx, tx.Safe, tx.SafeTxHash)
}

func (s *safeService) markExecuting(ctx context.Context, txs []safe.Transaction) {
	if s.executing == nil || len(txs) == 0 {
		return
	}
	hashes := make([]string, len(txs))
	for i, tx := range txs {
		hashes[i] = strings.ToLower(tx.SafeTxHash)
	}
	queued := s.executing(ctx, hashes)
	for i := range txs {
		txs[i].Executing = queued[hashes[i]]
	}
}

func (s *safeService) confirm(ctx context.Context, caller string, req safeConfirmationRequest) (*safe.Transaction, error) {
	if !safeTxHashPattern.MatchString(req.SafeTxHash) {
		return nil, badRequest("invalid safeTxHash")
	}
	tx, err := s.sts.Transaction(ctx, req.SafeTxHash)
	if err != nil {
		return nil, upstreamError(err)
	}
	if !common.IsHexAddress(tx.Safe) {
		return nil, upstreamError(errors.New("malformed upstream transaction"))
	}
	safeAddress := common.HexToAddress(tx.Safe)
	if err := s.requireSigner(ctx, caller, safeAddress); err != nil {
		return nil, err
	}
	// The stored transaction is judged again: a transaction proposed outside
	// this proxy (the Safe app) must not gain confirmations through it unless
	// it is one policy would have accepted.
	hash, err := s.verifyTransaction(ctx, safeAddress, req.SafeTxHash, tx.TxFields)
	if err != nil {
		return nil, err
	}
	signer, err := s.signerOf(ctx, safeAddress, hash, req.Signature)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(signer.Hex(), caller) {
		return nil, forbidden("signature was not made by the signed-in account")
	}
	if err := s.sts.Confirm(ctx, hash.Hex(), req.Signature); err != nil {
		return nil, upstreamError(err)
	}
	updated, err := s.sts.Transaction(ctx, hash.Hex())
	if err != nil {
		return nil, upstreamError(err)
	}
	s.queueExecution(ctx, updated)
	return updated, nil
}

func (s *safeService) pending(ctx context.Context, caller, safeAddress string) ([]safe.Transaction, error) {
	if !common.IsHexAddress(safeAddress) {
		return nil, badRequest("invalid Safe address")
	}
	address := common.HexToAddress(safeAddress)
	if err := s.requireSigner(ctx, caller, address); err != nil {
		return nil, err
	}
	nonce, err := s.sts.Nonce(ctx, address.Hex())
	if err != nil {
		return nil, upstreamError(err)
	}
	queued, err := s.sts.Queued(ctx, address.Hex(), nonce)
	if err != nil {
		return nil, upstreamError(err)
	}
	results := make([]safe.Transaction, 0, len(queued))
	for _, tx := range queued {
		if strings.EqualFold(tx.Safe, address.Hex()) && !tx.IsExecuted {
			results = append(results, tx)
		}
	}
	s.markExecuting(ctx, results)
	// A transaction can reach its threshold while execution is off, or before
	// clc-core could queue it. Listing queues it; the job coalesces per hash.
	for i := range results {
		if !results[i].Executing {
			s.queueExecution(ctx, &results[i])
		}
	}
	return results, nil
}

func (s *safeService) transaction(ctx context.Context, caller, safeTxHash string) (*safe.Transaction, error) {
	if !safeTxHashPattern.MatchString(safeTxHash) {
		return nil, badRequest("invalid safeTxHash")
	}
	tx, err := s.sts.Transaction(ctx, safeTxHash)
	if err != nil {
		return nil, upstreamError(err)
	}
	if !common.IsHexAddress(tx.Safe) {
		return nil, upstreamError(errors.New("malformed upstream transaction"))
	}
	if err := s.requireSigner(ctx, caller, common.HexToAddress(tx.Safe)); err != nil {
		return nil, err
	}
	single := []safe.Transaction{*tx}
	s.markExecuting(ctx, single)
	return &single[0], nil
}

func (s storeSafeSigners) Signers(ctx context.Context, safeAddress common.Address) ([]string, bool, error) {
	tx, err := s.a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	state, err := s.a.ensureSafeState(ctx, tx, safeAddress.Hex(), false)
	if err != nil || state == nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return state.Signers, true, nil
}

// ensureSafeState reads the durable tracker projection. First inspection seeds
// an existing multisig, and explicit inspections refresh custom implementations
// that may not emit Safe owner events. Projection age never expires permissions.
func (a *API) ensureSafeState(ctx context.Context, tx pgx.Tx, safeAddress string, inspect bool) (*store.SafeState, error) {
	state, err := a.store.GetSafeState(ctx, tx, a.chainID, safeAddress)
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if known && (!inspect || a.safeReader == nil) {
		return &state, nil
	}
	if a.safeReader == nil || (!known && !inspect && a.safeMisses.recent(safeAddress)) {
		return nil, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, safeReadTimeout)
	defer cancel()
	read, block, err := a.safeReader.ReadSafeLatest(readCtx, common.HexToAddress(safeAddress))
	if errors.Is(err, safe.ErrNotSafe) {
		a.safeMisses.add(safeAddress)
		return nil, nil
	}
	if err != nil {
		// A visitor refresh must not discard a durable event projection just
		// because RPC is temporarily unavailable.
		if known {
			return &state, nil
		}
		return nil, err
	}
	signers := make([]string, len(read.Owners))
	for i, owner := range read.Owners {
		signers[i] = owner.Hex()
	}
	if math.MaxInt64 < block {
		return nil, errors.New("block out of range")
	}
	if _, err := a.store.ReplaceSafeSigners(ctx, tx, a.chainID, safeAddress, signers, read.Threshold, block, math.MaxInt64); err != nil {
		return nil, err
	}
	refreshed, err := a.store.GetSafeState(ctx, tx, a.chainID, safeAddress)
	if err != nil {
		return nil, err
	}
	return &refreshed, nil
}

// safeOwnerHandler is independent of STS: metadata and public ownership must
// work even where transaction proposals are unavailable. Only a real catalog
// owner can trigger a sponsored AccountIndex registration.
func (a *API) safeOwnerHandler(c *echo.Context) error {
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return a.writeProxyError(c, badRequest("invalid owner address"))
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return handlePostgresError(c, err)
	}
	defer tx.Rollback(ctx)
	owned, err := a.store.CatalogHasOwner(ctx, tx, a.chainID, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	result := map[string]any{"kind": "unsupported", "owner": strings.ToLower(address)}
	if owned {
		state, err := a.ensureSafeState(ctx, tx, address, true)
		if err != nil {
			return a.writeProxyError(c, policyError(err))
		}
		if state != nil {
			if err := worker.EnqueueSafeRegistration(ctx, a.queueClient, tx, address); err != nil {
				return handlePostgresError(c, err)
			}
			var threshold *int
			if state.Threshold > 0 {
				threshold = &state.Threshold
			}
			result = map[string]any{"kind": "safe", "owner": state.Address, "signers": state.Signers, "threshold": threshold}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return a.writeProxyResult(c, "Multisig ownership", result)
}

// userSafesHandler lists the multisigs the caller signs for, each in the shape
// /owners/:address answers for a Safe, so the app manages their signers from
// the same projection that authorizes its writes.
func (a *API) userSafesHandler(c *echo.Context) error {
	_, address, _ := getUserClaims(c)
	c.Response().Header().Set("Cache-Control", "no-store")
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return handlePostgresError(c, err)
	}
	defer tx.Rollback(ctx)
	states, err := a.store.ListSafesSignedBy(ctx, tx, a.chainID, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	safes := make([]map[string]any, len(states))
	for i, state := range states {
		var threshold *int
		if state.Threshold > 0 {
			threshold = &state.Threshold
		}
		safes[i] = map[string]any{"kind": "safe", "owner": state.Address, "signers": state.Signers, "threshold": threshold}
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Multisigs you sign for", Result: map[string]any{"safes": safes},
	})
}

func (a *API) writeProxyError(c *echo.Context, err error) error {
	var pErr *proxyError
	if !errors.As(err, &pErr) {
		a.logg.Error("safe proxy failed", "err", err)
		pErr = &proxyError{http.StatusInternalServerError, apiresp.ErrCodeInternalServerError, "internal error"}
	}
	if jErr := c.JSON(pErr.status, apiresp.ErrResponse{Ok: false, ErrCode: pErr.code, Description: pErr.message}); jErr != nil {
		return jErr
	}
	return pErr
}

// writeProxyResult wraps result in the standard envelope, whose Result is an
// object: a transaction is flattened into it, a map is used as is.
func (a *API) writeProxyResult(c *echo.Context, description string, result any) error {
	object, isObject := result.(map[string]any)
	if !isObject {
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: description, Result: object})
}

func (a *API) safeProposalHandler(c *echo.Context) error {
	var req safeProposalRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	_, caller, _ := getUserClaims(c)
	tx, err := a.safeProxy.propose(c.Request().Context(), caller, req)
	if err != nil {
		return a.writeProxyError(c, err)
	}
	return a.writeProxyResult(c, "Safe transaction proposed", tx)
}

func (a *API) safeConfirmationHandler(c *echo.Context) error {
	var req safeConfirmationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	_, caller, _ := getUserClaims(c)
	tx, err := a.safeProxy.confirm(c.Request().Context(), caller, req)
	if err != nil {
		return a.writeProxyError(c, err)
	}
	return a.writeProxyResult(c, "Safe transaction confirmed", tx)
}

func (a *API) safePendingHandler(c *echo.Context) error {
	_, caller, _ := getUserClaims(c)
	results, err := a.safeProxy.pending(c.Request().Context(), caller, c.Param("address"))
	if err != nil {
		return a.writeProxyError(c, err)
	}
	return a.writeProxyResult(c, "Pending Safe transactions", map[string]any{"results": results})
}

func (a *API) safeTransactionHandler(c *echo.Context) error {
	_, caller, _ := getUserClaims(c)
	tx, err := a.safeProxy.transaction(c.Request().Context(), caller, c.Param("safeTxHash"))
	if err != nil {
		return a.writeProxyError(c, err)
	}
	return a.writeProxyResult(c, "Safe transaction", tx)
}
