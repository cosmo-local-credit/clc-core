package api

import (
	"net/http"
	"strings"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

type (
	resourceContactFields struct {
		Email   *string
		Phone   *string
		Website *string
	}
)

// protectResourceContacts keeps assign-all profile writes compatible without
// allowing an older or stale editor to erase contacts it never loaded. A clear
// is accepted only when the request names that field explicitly.
func protectResourceContacts(
	c *echo.Context,
	current resourceContactFields,
	requested resourceContactFields,
	clearFields []string,
) (resourceContactFields, bool, error) {
	clears := make(map[string]struct{}, len(clearFields))
	for _, field := range clearFields {
		if _, duplicate := clears[field]; duplicate {
			return requested, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeValidationFailed,
				Description: "clearContactFields must not contain duplicates",
			})
		}
		clears[field] = struct{}{}
	}

	var handled bool
	var err error
	requested.Email, handled, err = protectResourceContactField(c, "email", current.Email, requested.Email, clears)
	if handled || err != nil {
		return requested, handled, err
	}
	requested.Phone, handled, err = protectResourceContactField(c, "phone", current.Phone, requested.Phone, clears)
	if handled || err != nil {
		return requested, handled, err
	}
	requested.Website, handled, err = protectResourceContactField(c, "website", current.Website, requested.Website, clears)
	return requested, handled, err
}

func protectResourceContactField(
	c *echo.Context,
	field string,
	current, requested *string,
	clears map[string]struct{},
) (*string, bool, error) {
	_, clearing := clears[field]
	requestedHasValue := requested != nil && strings.TrimSpace(*requested) != ""
	currentHasValue := current != nil && strings.TrimSpace(*current) != ""

	if clearing {
		if requestedHasValue {
			return requested, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeValidationFailed,
				Description: "a contact field cannot be set and cleared in the same request",
			})
		}
		return nil, false, nil
	}
	// An omitted field is a legacy partial write. Preserve the stored value so
	// the request remains safe without pretending omission was explicit consent
	// to publish less information. Updated editors use ClearContactFields.
	if requested == nil {
		return current, false, nil
	}
	if currentHasValue && !requestedHasValue {
		return requested, true, handleConflictError(c,
			"contact information changed; refresh the resource and explicitly clear removed contact fields")
	}
	if !requestedHasValue {
		return nil, false, nil
	}
	return requested, false, nil
}
