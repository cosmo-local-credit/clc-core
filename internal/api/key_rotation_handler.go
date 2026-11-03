package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

var (
	// errKeyRotationProof is any proof that does not authorize the rotation it
	// names. The response never says which check failed beyond the description.
	errKeyRotationProof = errors.New("key rotation proof rejected")
)

type (
	keyRotationPasskey struct {
		CredentialID string `json:"credentialId"`
		PublicKeyX   string `json:"publicKeyX"`
		PublicKeyY   string `json:"publicKeyY"`
	}

	keyRotationAddresses struct {
		Old common.Address
		New common.Address
	}
)

// keyRotationHandler moves the caller's account from a compromised sign-in
// card to a new one (GH clc-app#412). A Calibur root key cannot be revoked, so
// the account changes address instead; the client moves the old wallet's
// assets afterwards with the old key.
//
// It authenticates by proofs, not by the session cookie: a stolen cookie must
// not be enough to move an account, and a client that lost the response can
// retry with fresh proofs after its old session stopped working. Both proofs
// sign the same statement naming both addresses. When the account has an
// active passkey, the old card's signature alone is refused: whoever
// compromised the card does not hold the passkey.
func (a *API) keyRotationHandler(c *echo.Context) error {
	var req apiresp.KeyRotationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	if (req.Old.SIWE == nil) == (req.Old.WebAuthn == nil) {
		return keyRotationProofRejected(c, "exactly one proof of the old sign-in card is required")
	}

	addresses, err := a.verifyKeyRotationSIWE(req.New, func(addresses keyRotationAddresses, signer common.Address) bool {
		return signer == addresses.New
	})
	if err != nil {
		return keyRotationProofRejected(c, "the new sign-in card's signature is not valid for this replacement")
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.keyRotationAccount(ctx, tx, addresses)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrNoRecordFound,
			Description: "no account holds this sign-in card",
		})
	}
	if err != nil {
		return err
	}
	if user.Status != "APPROVED" {
		return handleForbiddenError(c, "only an approved account can replace its sign-in card")
	}

	if req.Old.WebAuthn != nil {
		if err := a.verifyKeyRotationPasskey(ctx, tx, *req.Old.WebAuthn, addresses); err != nil {
			return keyRotationProofRejected(c, "the passkey could not confirm this replacement")
		}
	} else {
		// The statement must be the new card's statement, word for word.
		if _, err := a.verifyKeyRotationSIWE(*req.Old.SIWE, func(old keyRotationAddresses, signer common.Address) bool {
			return old == addresses && signer == addresses.Old
		}); err != nil {
			return keyRotationProofRejected(c, "the old sign-in card's signature is not valid for this replacement")
		}
		hasPasskey, err := a.hasActivePasskey(ctx, tx, addresses.Old)
		if err != nil {
			return err
		}
		if hasPasskey {
			return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeForbidden,
				Description: "confirm with your passkey to replace this sign-in card",
				Result:      map[string]any{"passkeyRequired": true},
			})
		}
	}

	result, err := a.rotateAddress(ctx, tx, store.AddressRotationRequest{
		UserID:      user.ID,
		OldAddress:  addresses.Old.Hex(),
		NewAddress:  addresses.New.Hex(),
		InitiatedBy: store.RotationByUser,
	})
	if err != nil {
		return keyRotationFailure(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	tokenStr, err := a.generateUserToken(result.User)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Sign-in card replaced",
		Result:      keyRotationResponse(result),
	})
}

// adminKeyRotationHandler moves an account to a new card when nothing can sign
// for the old one (a forgotten password and no passkey). The old wallet's
// assets stay where they are until the old card turns up. SUPERADMIN only, and
// never onto another SUPERADMIN or the operator's own account: the power to
// re-key an account is the power to take it over.
func (a *API) adminKeyRotationHandler(c *echo.Context) error {
	var req apiresp.AdminKeyRotationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	target := c.Param("address")
	if !common.IsHexAddress(target) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address must be a valid Ethereum address",
		})
	}
	targetAddress := common.HexToAddress(target)

	addresses, err := a.verifyKeyRotationSIWE(req.New, func(addresses keyRotationAddresses, signer common.Address) bool {
		return signer == addresses.New && addresses.Old == targetAddress
	})
	if err != nil {
		return keyRotationProofRejected(c, "the new sign-in card's signature is not valid for this replacement")
	}

	callerID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.keyRotationAccount(ctx, tx, addresses)
	if errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}
	if err != nil {
		return err
	}
	if user.ID == callerID {
		return handleForbiddenError(c, "replace your own sign-in card from the app")
	}
	if store.IsSuperadminRole(user.Role) {
		return handleForbiddenError(c, "a superadmin's sign-in card cannot be replaced from the console")
	}

	result, err := a.rotateAddress(ctx, tx, store.AddressRotationRequest{
		UserID:      user.ID,
		OldAddress:  addresses.Old.Hex(),
		NewAddress:  addresses.New.Hex(),
		InitiatedBy: store.RotationByAdmin,
		AdminUserID: &callerID,
	})
	if err != nil {
		return keyRotationFailure(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	a.logg.Info("admin replaced sign-in card",
		"userId", user.ID, "oldAddress", addresses.Old.Hex(), "newAddress", addresses.New.Hex(),
		"adminUserId", callerID, "replayed", result.Replayed)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Sign-in card replaced",
		Result:      keyRotationResponse(result),
	})
}

// userKeyRotationsHandler lists the addresses the caller's account moved off,
// so the app can offer to move what is left on an old card.
func (a *API) userKeyRotationsHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rotations, err := a.store.ListAddressRotationsByUser(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Replaced sign-in cards",
		Result:      map[string]any{"rotations": rotations},
	})
}

// verifyKeyRotationSIWE checks a proof's signature, statement, chain and
// nonce, and returns the addresses its statement names. accept decides
// whether the recovered signer may make this proof; the nonce is consumed only
// once everything else holds, so a rejected proof cannot burn it for the
// caller's next attempt.
func (a *API) verifyKeyRotationSIWE(
	proof apiresp.KeyRotationSIWEProof,
	accept func(keyRotationAddresses, common.Address) bool,
) (keyRotationAddresses, error) {
	var addresses keyRotationAddresses
	signer, err := util.VerifySIWESignature(proof.Message, proof.Signature)
	if err != nil {
		return addresses, err
	}
	claimed, err := util.ParseSIWEAddress(proof.Message)
	if err != nil || !strings.EqualFold(signer, claimed) {
		return addresses, errKeyRotationProof
	}
	addresses.Old, addresses.New, err = util.ParseKeyRotationStatement(util.ParseSIWEStatement(proof.Message))
	if err != nil {
		return addresses, err
	}
	chainID, err := util.ParseSIWEChainID(proof.Message)
	if err != nil || a.chainID < 0 || chainID != uint64(a.chainID) {
		return addresses, errKeyRotationProof
	}
	if !accept(addresses, common.HexToAddress(signer)) {
		return addresses, errKeyRotationProof
	}
	nonce, err := util.ParseSIWENonce(proof.Message)
	if err != nil || !a.siweNonces.Consume(nonce) {
		return addresses, errKeyRotationProof
	}
	return addresses, nil
}

// verifyKeyRotationPasskey accepts an assertion by a passkey confirmed on the
// old wallet, or — for a retry of a rotation that already committed — by the
// same passkey now waiting on the new one.
func (a *API) verifyKeyRotationPasskey(ctx context.Context, tx pgx.Tx, proof apiresp.WebAuthnRecoverRequest, addresses keyRotationAddresses) error {
	cred, err := a.store.GetWebAuthnCredential(ctx, tx, proof.CredentialID)
	if err != nil {
		return err
	}
	owner := common.HexToAddress(cred.Address)
	current := cred.State == store.WebAuthnActive && owner == addresses.Old
	retried := cred.State == store.WebAuthnMoved && owner == addresses.New
	if !current && !retried {
		return errKeyRotationProof
	}
	return a.relayer.VerifyAssertion(ctx, relayer.VerifyAssertionArgs{
		AuthenticatorData: proof.AuthenticatorData,
		ClientDataJSON:    proof.ClientDataJSON,
		Signature:         proof.Signature,
		PublicKeyX:        cred.PublicKeyX,
		PublicKeyY:        cred.PublicKeyY,
	})
}

// keyRotationAccount finds the account a rotation applies to: the one on the
// old address, or, when that rotation already committed, the one now on the
// new address. RotateUserAddress decides whether the latter is a true retry.
func (a *API) keyRotationAccount(ctx context.Context, tx pgx.Tx, addresses keyRotationAddresses) (store.User, error) {
	user, err := a.store.GetUserByAddress(ctx, tx, addresses.Old.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		return a.store.GetUserByAddress(ctx, tx, addresses.New.Hex())
	}
	return user, err
}

func (a *API) hasActivePasskey(ctx context.Context, tx pgx.Tx, address common.Address) (bool, error) {
	creds, err := a.store.ListWebAuthnCredentialsByAddress(ctx, tx, address.Hex())
	if err != nil {
		return false, err
	}
	for _, cred := range creds {
		if cred.State == store.WebAuthnActive {
			return true, nil
		}
	}
	return false, nil
}

// rotateAddress runs the move and, the first time, puts the new address in the
// AccountIndex in the same transaction: eth-tracker follows stablecoin
// transfers only for indexed accounts, and the new wallet may never relay a
// delegation of its own (which is how signup gets indexed).
func (a *API) rotateAddress(ctx context.Context, tx pgx.Tx, req store.AddressRotationRequest) (store.AddressRotationResult, error) {
	result, err := a.store.RotateUserAddress(ctx, tx, req)
	if err != nil || result.Replayed {
		return result, err
	}
	return result, worker.EnqueueSafeRegistration(ctx, a.queueClient, tx, req.NewAddress)
}

func keyRotationResponse(result store.AddressRotationResult) map[string]any {
	passkeys := make([]keyRotationPasskey, 0, len(result.MovedPasskeys))
	for _, cred := range result.MovedPasskeys {
		passkeys = append(passkeys, keyRotationPasskey{
			CredentialID: cred.CredentialID,
			PublicKeyX:   cred.PublicKeyX,
			PublicKeyY:   cred.PublicKeyY,
		})
	}
	return map[string]any{
		"userId":        result.User.ID,
		"role":          result.User.Role,
		"address":       common.HexToAddress(result.User.EthereumAddress).Hex(),
		"oldAddress":    common.HexToAddress(result.Rotation.OldAddress).Hex(),
		"movedPasskeys": passkeys,
		"replayed":      result.Replayed,
	}
}

func keyRotationProofRejected(c *echo.Context, description string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: description,
	})
}

func keyRotationFailure(c *echo.Context, err error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, store.ErrAddressRetired):
		return addressRetired(c)
	case errors.Is(err, store.ErrAddressRotationStale):
		return handleConflictError(c, "this account no longer uses that sign-in card")
	case errors.Is(err, store.ErrAddressRotationTargetTaken),
		isRetiredAddressError(err),
		errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation &&
			(pgErr.ConstraintName == "users_blockchain_address_unique" || pgErr.ConstraintName == "users_blockchain_address_lower_key"):
		return handleConflictError(c, "the new sign-in card already belongs to an account")
	case errors.Is(err, store.ErrAddressRotationDeploying):
		return handleConflictError(c, "wait for your pool or voucher to finish deploying")
	}
	return handlePostgresError(c, err)
}
