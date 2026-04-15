package api

import (
	"github.com/go-playground/validator/v10"
)

type Validator struct {
	ValidatorProvider *validator.Validate
}

func (v *Validator) Validate(i interface{}) error {
	return v.ValidatorProvider.Struct(i)
}
