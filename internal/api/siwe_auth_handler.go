package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func (a *API) nonceHandler(c *echo.Context) error {
	nonce := a.siweNonces.Generate()
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Nonce generated for SIWE challenge",
		Result: map[string]any{
			"nonce": nonce,
		},
	})
}

func (a *API) signupHandler(c *echo.Context) error {
	var req apiresp.SignupRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// 1. Verify SIWE signature — recover the signer address.
	signerAddr, err := util.VerifySIWESignature(req.Message, req.Signature)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "signature verification failed: " + err.Error(),
		})
	}

	// 2. Confirm the address embedded in the message matches the recovered signer.
	msgAddr, err := util.ParseSIWEAddress(req.Message)
	if err != nil || !strings.EqualFold(signerAddr, msgAddr) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address mismatch: signer does not match claimed address",
		})
	}

	// 3. Consume nonce (one-time use, 5-min TTL).
	nonce, err := util.ParseSIWENonce(req.Message)
	if err != nil || !a.siweNonces.Consume(nonce) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid or expired nonce",
		})
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 4. Guard against duplicate registrations.
	if _, lookupErr := a.store.GetUserByAddress(ctx, tx, signerAddr); lookupErr == nil {
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeConflict,
			Description: "address already linked to an existing account",
		})
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}

	// 5. Email is optional per the data model but the request validator marks
	//    it required, so it is always present here. Treat an email already
	//    bound to another wallet as a duplicate-identity conflict.
	if _, lookupErr := a.store.GetUserByEmail(ctx, tx, strings.ToLower(req.Email)); lookupErr == nil {
		// Distinct code from the address-conflict branch above: the caller's
		// address is not registered, so a SIWE sign-in is no recovery path. It
		// would register the wallet under placeholders, not under this email.
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeEmailConflict,
			Description: "email already linked to another account",
		})
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}

	// 6. Persist the new user. Either unique constraint (blockchain_address
	//    or email) can still trip from a concurrent registration raced
	//    between the lookup above and this insert — translate that into a
	//    409 rather than surfacing a generic 500. Distinguish by constraint
	//    name so the caller still knows whether its address ended up
	//    registered (safe to recover via sign-in) or not (email raced).
	user, err := a.store.InsertUser(ctx, tx, store.User{
		EthereumAddress: signerAddr,
		Name:            req.Name,
		Email:           strings.ToLower(req.Email),
		Role:            "USER",
		Status:          "APPROVED",
	})
	if err != nil {
		if conflict, ok := userIdentityConflict(err); ok {
			return c.JSON(http.StatusConflict, conflict)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Sign-up auto-approves, so issue the session cookie immediately — a new
	// user is authenticated without a separate sign-in round-trip.
	tokenStr, err := a.generateUserToken(user)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Registration successful",
		Result: map[string]any{
			"userId": user.ID,
			"status": user.Status,
			"role":   user.Role,
		},
	})
}

func (a *API) signinHandler(c *echo.Context) error {
	var req apiresp.SigninRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// 1. Verify SIWE signature.
	signerAddr, err := util.VerifySIWESignature(req.Message, req.Signature)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "signature verification failed: " + err.Error(),
		})
	}

	// 2. Address consistency check.
	msgAddr, err := util.ParseSIWEAddress(req.Message)
	if err != nil || !strings.EqualFold(signerAddr, msgAddr) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address mismatch: signer does not match claimed address",
		})
	}

	// 3. Consume nonce.
	nonce, err := util.ParseSIWENonce(req.Message)
	if err != nil || !a.siweNonces.Consume(nonce) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid or expired nonce",
		})
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 4. Look up the user, registering a wallet that has no account yet. Any key
	//    in the paper-wallet format must be able to sign in: a holder whose
	//    signup failed after the wallet was saved would otherwise be left with a
	//    working key and no way in.
	user, err := a.store.GetUserByAddress(ctx, tx, signerAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		user, err = a.registerPaperWallet(ctx, tx, signerAddr)
		if err == nil {
			a.logg.Info("registered wallet at sign-in", "address", signerAddr, "userId", user.ID)
		}
	}
	if err != nil {
		if conflict, ok := userIdentityConflict(err); ok {
			return c.JSON(http.StatusConflict, conflict)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// 5. Gate on approval status.
	switch user.Status {
	case "PENDING":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account is pending approval by a superadmin",
		})
	case "REJECTED":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account has been rejected",
		})
	}

	// 6. Issue JWT and set cookie.
	tokenStr, err := a.generateUserToken(user)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Signed in successfully",
		Result: map[string]any{
			"userId": user.ID,
			"role":   user.Role,
		},
	})
}

const (
	// paperWalletPlaceholderName is the name a sign-in registers a new wallet
	// under. It is PAPER_WALLET_PLACEHOLDER_NAME in clc-app's @clc/core: the bulk
	// sign-in card generator registers its cards with it too, and the app treats
	// it, and the pendingSIWEEmail address, as details the holder has yet to give.
	paperWalletPlaceholderName = "Sign-in card"
)

// registerPaperWallet creates the account for a wallet signing in without one.
//
// The insert runs in a savepoint. When a concurrent sign-in for the same wallet
// commits first, the unique violation rolls back only the savepoint, and the
// account the other request created is read back so both sign in to it. A
// conflict that leaves no account for the address is someone else holding the
// placeholder email, which is returned as the conflict it is.
func (a *API) registerPaperWallet(ctx context.Context, tx pgx.Tx, address string) (store.User, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return store.User{}, err
	}
	defer savepoint.Rollback(ctx)

	user, err := a.store.InsertUser(ctx, savepoint, store.User{
		EthereumAddress: address,
		Name:            paperWalletPlaceholderName,
		Email:           pendingSIWEEmail(address),
		Role:            "USER",
		Status:          "APPROVED",
	})
	if err == nil {
		return user, savepoint.Commit(ctx)
	}
	if _, ok := userIdentityConflict(err); !ok {
		return store.User{}, err
	}
	if rollbackErr := savepoint.Rollback(ctx); rollbackErr != nil {
		return store.User{}, rollbackErr
	}

	existing, lookupErr := a.store.GetUserByAddress(ctx, tx, address)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return store.User{}, err
	}
	return existing, lookupErr
}

func (a *API) logoutHandler(c *echo.Context) error {
	a.clearUserAuthCookie(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Signed out successfully",
		Result:      map[string]any{},
	})
}

// webAuthnSigninHandler verifies a WebAuthn assertion against a stored
// credential and, for a registered + approved user, issues the session cookie.
//
// This is the passkey analogue of signinHandler. A passkey assertion is itself
// an authentication (device-bound credential + user verification), so — unlike
// a raw private key / paper wallet, which anyone can load and therefore must
// prove control of via a SIWE signature — no SIWE message is required. The
// challenge is issued by the relayer WebAuthn challenge endpoint and consumed
// by VerifyAssertion (shared relayer challenge store).
func (a *API) webAuthnSigninHandler(c *echo.Context) error {
	var req apiresp.WebAuthnRecoverRequest
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

	// 1. Resolve the credential -> wallet address + P-256 public key.
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

	// 2. Verify the WebAuthn assertion — the authentication step, equivalent to
	//    SIWE ecrecover for EOAs.
	if err := a.relayer.VerifyAssertion(ctx, relayer.VerifyAssertionArgs{
		AuthenticatorData: req.AuthenticatorData,
		ClientDataJSON:    req.ClientDataJSON,
		Signature:         req.Signature,
		PublicKeyX:        cred.PublicKeyX,
		PublicKeyY:        cred.PublicKeyY,
	}); err != nil {
		a.logg.Warn("webauthn signin verification failed",
			"credentialId", req.CredentialID,
			"err", err,
		)
		return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeWebAuthnVerificationFailed,
			Description: "WebAuthn verification failed",
		})
	}

	// 3. Look up the registered user behind the recovered address. cred.Address
	//    is the checksummed canonical form; GetUserByAddress matches
	//    case-insensitively, so it resolves regardless of stored casing.
	user, err := a.store.GetUserByAddress(ctx, tx, cred.Address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "address not registered, please sign up first",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// 4. Gate on approval status (mirrors signinHandler).
	switch user.Status {
	case "PENDING":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account is pending approval by a superadmin",
		})
	case "REJECTED":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account has been rejected",
		})
	}

	// 5. Issue JWT and set the session cookie.
	tokenStr, err := a.generateUserToken(user)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Signed in successfully",
		Result: map[string]any{
			"userId":     user.ID,
			"role":       user.Role,
			"address":    cred.Address,
			"keyHash":    cred.KeyHash,
			"publicKeyX": cred.PublicKeyX,
			"publicKeyY": cred.PublicKeyY,
		},
	})
}

// sessionHandler returns the current session's address, chainId and role.
// Returns 204 No Content when no valid session cookie is present.
func (a *API) sessionHandler(c *echo.Context) error {
	cookie, err := c.Cookie(userAuthCookieName)
	if err != nil || cookie.Value == "" {
		return c.NoContent(http.StatusNoContent)
	}

	token, parseErr := a.parseUserJWT(cookie.Value)
	if parseErr != nil {
		return c.NoContent(http.StatusNoContent)
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := a.store.GetUserByID(ctx, tx, token.UserID)
	if err != nil || user.Status != "APPROVED" || !strings.EqualFold(user.EthereumAddress, token.EthereumAddress) {
		a.clearUserAuthCookie(c)
		return c.NoContent(http.StatusNoContent)
	}
	if user.Role != token.Role {
		refreshed, signErr := a.generateUserToken(user)
		if signErr != nil {
			return signErr
		}
		a.setUserAuthCookie(c, refreshed)
	}

	return c.JSON(http.StatusOK, map[string]any{
		// Always emit the EIP-55 checksummed form, even for legacy lowercase
		// tokens, so clients receive a consistent canonical address.
		"address": common.HexToAddress(user.EthereumAddress).Hex(),
		"chainId": a.chainID,
		// The role the signin response already returns. Without it a client that
		// gates on privilege has to re-derive it after every reload by probing an
		// admin endpoint and reading the 403.
		"role": user.Role,
	})
}

// webAuthnCredentialsHandler lists the passkeys mapped to the signed-in
// wallet, for the holder's Settings screen.
//
// It sits on the public /auth group beside sessionHandler and resolves the
// session the same way, inside the handler: the group has no middleware, and
// flexibleAuthMiddleware would also admit a service token, which has no wallet
// of its own. The address comes from the session only, so one account can
// never read another's credentials.
//
// State is what clc-core has persisted — pending until the register() call is
// confirmed, then active. There is deliberately no chain read here; a pending
// row keeps its tracking id so a client that reloaded can resume following it.
func (a *API) webAuthnCredentialsHandler(c *echo.Context) error {
	cookie, err := c.Cookie(userAuthCookieName)
	if err != nil || cookie.Value == "" {
		return webAuthnCredentialsUnauthorized(c)
	}
	token, err := a.parseUserJWT(cookie.Value)
	if err != nil || !common.IsHexAddress(token.EthereumAddress) {
		return webAuthnCredentialsUnauthorized(c)
	}
	address := common.HexToAddress(token.EthereumAddress).Hex()

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	creds, err := a.store.ListWebAuthnCredentialsByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "WebAuthn credentials",
		Result: map[string]any{
			"address":     address,
			"credentials": webAuthnCredentialSummaries(creds),
		},
	})
}

// webAuthnCredentialSummary is the public shape of one credential row. The
// tracking id is only meaningful, and only sent, while the row is pending.
type (
	webAuthnCredentialSummary struct {
		CredentialID string    `json:"credentialId"`
		State        string    `json:"state"`
		CreatedAt    time.Time `json:"createdAt"`
		TrackingID   *string   `json:"trackingId"`
	}
)

func webAuthnCredentialSummaries(creds []store.WebAuthnCredential) []webAuthnCredentialSummary {
	out := make([]webAuthnCredentialSummary, 0, len(creds))
	for _, cred := range creds {
		summary := webAuthnCredentialSummary{
			CredentialID: cred.CredentialID,
			State:        cred.State,
			CreatedAt:    cred.CreatedAt,
		}
		if cred.State == store.WebAuthnPending {
			summary.TrackingID = cred.TrackingID
		}
		out = append(out, summary)
	}
	return out
}

func webAuthnCredentialsUnauthorized(c *echo.Context) error {
	return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrJWTAuth,
		Description: "a user session is required",
	})
}
