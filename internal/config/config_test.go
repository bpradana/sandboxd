package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAreValid(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Sandbox.ImagesDir != "/var/lib/sandboxd/images" || cfg.Network.CIDR != "10.88.0.0/16" {
		t.Fatalf("unexpected defaults: %+v %+v", cfg.Sandbox, cfg.Network)
	}
	if cfg.Exec.MaxTerminals != 4 || cfg.App.UIEnabled {
		t.Fatalf("unexpected defaults: terminals %d, ui %v", cfg.Exec.MaxTerminals, cfg.App.UIEnabled)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"production without api keys": {"APP_ENV": "production", "APP_API_KEYS": ""},
		"short production api key":    {"APP_ENV": "production", "APP_API_KEYS": "short"},
		"default above maximum":       {"SANDBOX_DEFAULT_MEMORY_MB": "8192", "SANDBOX_MAX_MEMORY_MB": "1024"},
		"exec timeout above http":     {"SANDBOX_EXEC_MAX_TIMEOUT": "20m", "HTTP_WRITE_TIMEOUT": "15m"},
		"root identity mapping":       {"SANDBOX_UID_MAP_BASE": "0"},
		"exec uid outside range":      {"SANDBOX_EXEC_UID": "70000"},
		"invalid cidr":                {"SANDBOX_NETWORK_CIDR": "not-a-cidr"},
		"ipv6 cidr":                   {"SANDBOX_NETWORK_CIDR": "fd00::/64"},
		"invalid dns":                 {"SANDBOX_NETWORK_DNS": "dns.example"},
		"unknown otlp protocol":       {"OTEL_EXPORTER_OTLP_PROTOCOL": "datadog"},
		"no terminals":                {"SANDBOX_EXEC_MAX_TERMINALS": "0"},
		"no terminal detach window":   {"SANDBOX_TERMINAL_DETACH_TIMEOUT": "0s"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted an invalid configuration")
			}
		})
	}
}

func TestValidateExecConfigMentionsBothSettings(t *testing.T) {
	t.Parallel()

	err := validateExecConfig(ExecConfig{
		DefaultTimeout: time.Minute, MaxTimeout: time.Hour, MaxOutputBytes: 1, MaxConcurrent: 1, MaxSessions: 1, MaxTerminals: 1, TerminalDetachTimeout: time.Minute, FileMaxBytes: 1,
	}, HTTPConfig{WriteTimeout: time.Minute, StreamWriteTimeout: 2 * time.Hour})
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_EXEC_MAX_TIMEOUT") || !strings.Contains(err.Error(), "HTTP_WRITE_TIMEOUT") {
		t.Fatalf("error = %v", err)
	}
}
