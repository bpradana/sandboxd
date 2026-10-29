package fx

import (
	"testing"

	"go.uber.org/fx"
)

func TestModuleGraphIsValid(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	if err := fx.ValidateApp(Module, fx.NopLogger); err != nil {
		t.Fatalf("fx graph is invalid: %v", err)
	}
}
