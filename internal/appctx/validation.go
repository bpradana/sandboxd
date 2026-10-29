package appctx

import (
	"net/http"
)

func NewValidationError(err error) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, http.StatusText(http.StatusBadRequest)).
		WithErrors(ExtractValidationErrors(err)).
		WithErrorCode(CodeValidation)
}
