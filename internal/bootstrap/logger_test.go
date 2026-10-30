package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Expected conditions are logged as warnings, and a warning is read, not
// debugged. zap's development preset stacks every one of them, which is what
// this pins shut.
func TestWarningsAreOneLineAndErrorsKeepTheirStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.json")
	zapCfg := zap.NewDevelopmentConfig()
	zapCfg.OutputPaths = []string{path}
	zapCfg.ErrorOutputPaths = []string{path}

	logger, err := buildZapLogger(zapCfg)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	logger.Warn("APP_API_KEYS is empty: the sandbox API is unauthenticated")
	if err := logger.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	warning, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(warning)), "\n"); lines != 0 {
		t.Fatalf("a warning spans %d extra lines:\n%s", lines, warning)
	}
	if strings.Contains(string(warning), "go.uber.org/fx") {
		t.Fatalf("a warning carries a stack trace:\n%s", warning)
	}

	logger.Error("something actually broke")
	if err := logger.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	all, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.TrimPrefix(string(all), string(warning)), "logger_test.go") {
		t.Fatalf("an error lost its stack trace:\n%s", all)
	}
}
