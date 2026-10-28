package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/unicode/norm"
)

var (
	errIdentityAvailabilityNull = errors.New("identity availability result was null")
	// Same shape the onboarding phone field accepts: leading +, no zero-prefixed
	// country code, 8–15 digits in total.
	e164Phone = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)
)

// identityAvailabilityHandler tells onboarding whether an email, phone, or
// username is still free. It is an advisory read, like the voucher and pool
// name lookup: signup and profile save repeat the check and are what actually
// claim the identity. The response names only the fields that were asked about,
// and it does not repeat their values or say who holds them.
//
// A signed-in caller's own identities are free. Signup creates the account and
// its session before onboarding uploads the photo, saves the profile and
// activates the wallet, so a user who goes back after one of those fails would
// otherwise be told their own email is taken.
func (a *API) identityAvailabilityHandler(c *echo.Context) error {
	email, phone, username, description := parseIdentityAvailabilityQuery(c)
	if description != "" {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: description,
		})
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	taken, err := a.store.IdentityAvailability(ctx, tx, email, phone, username, a.sessionOwnerKey(c))
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	result := map[string]any{}
	if email != nil {
		available, err := availabilityFlag(taken.Email)
		if err != nil {
			return err
		}
		result["email"] = map[string]any{"available": available}
	}
	if phone != nil {
		available, err := availabilityFlag(taken.Phone)
		if err != nil {
			return err
		}
		result["phone"] = map[string]any{"available": available}
	}
	if username != nil {
		available, err := availabilityFlag(taken.Username)
		if err != nil {
			return err
		}
		result["username"] = map[string]any{"available": available}
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Identity availability",
		Result:      result,
	})
}

// sessionOwnerKey is the session cookie's address, lowercased the way
// identity_claims keys a user, or nil for a guest. Resolved like
// webAuthnCredentialsHandler: from the cookie only, never a service bearer, and
// a cookie that does not verify is a guest rather than an error.
func (a *API) sessionOwnerKey(c *echo.Context) *string {
	cookie, err := c.Cookie(userAuthCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	token, err := a.parseUserJWT(cookie.Value)
	if err != nil || !common.IsHexAddress(token.EthereumAddress) {
		return nil
	}
	owner := strings.ToLower(common.HexToAddress(token.EthereumAddress).Hex())
	return &owner
}

func availabilityFlag(taken *bool) (bool, error) {
	if taken == nil {
		return false, errIdentityAvailabilityNull
	}
	return !*taken, nil
}

// parseIdentityAvailabilityQuery returns a non-empty description when the query
// should be rejected. Blank parameters are ignored. At least one identity has
// to remain, and any present identity has to be one signup would accept.
func parseIdentityAvailabilityQuery(c *echo.Context) (email, phone, username *string, description string) {
	rawEmail := strings.TrimSpace(c.QueryParam("email"))
	if rawEmail != "" {
		if err := c.Validate(&struct {
			Email string `validate:"required,email"`
		}{Email: rawEmail}); err != nil {
			return nil, nil, nil, "Validation failed: email"
		}
		lowered := strings.ToLower(rawEmail)
		email = &lowered
	}

	rawPhone := strings.TrimSpace(c.QueryParam("phone"))
	if rawPhone != "" {
		if !e164Phone.MatchString(rawPhone) {
			return nil, nil, nil, "Validation failed: phone"
		}
		phone = &rawPhone
	}

	rawUsername := c.QueryParam("username")
	if strings.TrimSpace(rawUsername) != "" {
		name := norm.NFC.String(strings.TrimSpace(rawUsername))
		if !store.ValidUsername(name) {
			return nil, nil, nil, "Validation failed: username"
		}
		username = &name
	}

	if email == nil && phone == nil && username == nil {
		return nil, nil, nil, "Validation failed: an email, phone, or username is required"
	}
	return email, phone, username, ""
}
