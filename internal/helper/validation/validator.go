// Package validation wraps go-playground/validator with the shared struct-tag naming and custom rules.
package validation

import (
	"errors"
	"reflect"
	"strings"
	"sync"

	validator "github.com/go-playground/validator/v10"
)

const tagNotBlank = "notblank"

var (
	once     sync.Once
	validate *validator.Validate
)

func Shared() *validator.Validate {
	once.Do(func() {
		v := validator.New(validator.WithRequiredStructEnabled())
		v.RegisterTagNameFunc(func(field reflect.StructField) string {
			for _, tagName := range []string{"json", "query", "param", "header", "form"} {
				name := strings.Split(field.Tag.Get(tagName), ",")[0]
				if name != "" && name != "-" {
					return name
				}
			}
			return field.Name
		})
		_ = v.RegisterValidation(tagNotBlank, func(fl validator.FieldLevel) bool {
			if fl.Field().Kind() != reflect.String {
				return false
			}
			return strings.TrimSpace(fl.Field().String()) != ""
		})
		validate = v
	})
	return validate
}

func Struct(value any) error {
	return Shared().Struct(value)
}

func IsValidationError(err error) bool {
	var validationErrs validator.ValidationErrors
	return errors.As(err, &validationErrs)
}

type ErrorDetail struct {
	Field string `json:"field"`
	Tag   string `json:"tag"`
	Value any    `json:"value,omitempty"`
	Param string `json:"param,omitempty"`
}

func Messages(err error) []string {
	if validationErrs, ok := errors.AsType[validator.ValidationErrors](err); ok {
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return messages
	}

	if err != nil {
		return []string{err.Error()}
	}

	return nil
}

func Details(err error) []ErrorDetail {
	var validationErrs validator.ValidationErrors
	if !errors.As(err, &validationErrs) {
		return nil
	}

	details := make([]ErrorDetail, 0, len(validationErrs))
	for _, validationErr := range validationErrs {
		details = append(details, ErrorDetail{
			Field: validationErr.Field(),
			Tag:   validationErr.Tag(),
			Value: validationErr.Value(),
			Param: validationErr.Param(),
		})
	}

	return details
}
