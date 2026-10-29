package httpapi

import (
	"github.com/bpradana/sandboxd/internal/helper/validation"
)

type requestValidator struct {
	validator interface {
		Struct(any) error
	}
}

func newRequestValidator() *requestValidator {
	return &requestValidator{validator: validation.Shared()}
}

func (v *requestValidator) Validate(i interface{}) error {
	return v.validator.Struct(i)
}
