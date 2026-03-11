package nextapi

import (
	"github.com/go-playground/validator/v10"
)

type Validator struct {
	ValidatorProvider *validator.Validate
}

func (v *Validator) Validate(i interface{}) error {
	return v.ValidatorProvider.Struct(i)
}

func (a *API) isBannedToken(tokenAddress string) bool {
	_, exists := a.bannedTokens[tokenAddress]
	return exists
}
