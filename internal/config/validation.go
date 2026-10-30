package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bpradana/sandboxd/internal/helper/validation"
)

func validateSnapshotConfig(cfg SnapshotConfig) error {
	if cfg.Enabled && (strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.Region) == "") {
		return fmt.Errorf("SNAPSHOT_S3_BUCKET and SNAPSHOT_S3_REGION must be provided")
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("SNAPSHOT_S3_ENDPOINT must be an http(s) URL without credentials, query, or fragment")
		}
	}
	if !filepath.IsAbs(cfg.TempDir) || !filepath.IsAbs(cfg.SettingsDir) || cfg.Timeout <= 0 || cfg.MaxConcurrent <= 0 || cfg.MaxBytes <= 0 || cfg.MaxBytes > 4<<30 {
		return fmt.Errorf("snapshot temp directory must be absolute, timeout and concurrency positive, and SNAPSHOT_MAX_BYTES between 1 and 4294967296")
	}
	return nil
}

var previewHostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// FrameSource returns the exact wildcard origin permitted to embed previews.
// Validate the hostname before using configuration in an HTTP CSP header.
func (cfg PreviewConfig) FrameSource() (string, error) {
	if !cfg.Enabled {
		return "'none'", nil
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || net.ParseIP(u.Hostname()) != nil || !previewHostname.MatchString(u.Hostname()) || len(u.Hostname()) > 200 {
		return "", fmt.Errorf("PREVIEW_BASE_URL must be an http(s) URL with a DNS hostname supporting wildcard subdomains, without a path or credentials")
	}
	return u.Scheme + "://*." + strings.ToLower(u.Host), nil
}

func validatePreviewConfig(cfg PreviewConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if _, err := cfg.FrameSource(); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(cfg.Address); err != nil || cfg.TTL <= 0 || cfg.TTL > 24*time.Hour || cfg.MaxSessions < 1 {
		return fmt.Errorf("PREVIEW_ADDRESS must be host:port, PREVIEW_TTL between 0 and 24h, and PREVIEW_MAX_SESSIONS positive")
	}
	return nil
}

func isProductionEnvironment(environment string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(environment)), "production")
}

func defaultDocsEnabled(environment string) bool {
	return !isProductionEnvironment(environment)
}

func validateStaticConfig(cfg Config) error {
	for _, item := range []any{
		cfg.Telemetry,
	} {
		if err := validation.Struct(item); err != nil {
			messages := validation.Messages(err)
			if len(messages) > 0 {
				return fmt.Errorf("%s", messages[0])
			}
			return err
		}
	}
	return nil
}

func validateAppConfig(cfg AppConfig) error {
	if isProductionEnvironment(cfg.Env) && len(cfg.APIKeys) == 0 {
		return fmt.Errorf("APP_API_KEYS must be provided in production")
	}
	for _, key := range cfg.APIKeys {
		if len(key) < 16 && isProductionEnvironment(cfg.Env) {
			return fmt.Errorf("APP_API_KEYS entries must be at least 16 characters in production")
		}
	}
	return nil
}

func validateSandboxConfig(cfg SandboxConfig) error {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return fmt.Errorf("SANDBOX_STATE_DIR must be provided")
	}
	if strings.TrimSpace(cfg.ImagesDir) == "" {
		return fmt.Errorf("SANDBOX_IMAGES_DIR must be provided")
	}
	if cfg.StorageDriver != "disk" && cfg.StorageDriver != "tmpfs" {
		return fmt.Errorf("SANDBOX_STORAGE_DRIVER must be disk or tmpfs")
	}
	if cfg.StorageFS != "" && cfg.StorageFS != "ext4" && cfg.StorageFS != "btrfs" {
		return fmt.Errorf("SANDBOX_STORAGE_FS must be ext4, btrfs, or empty")
	}
	if cfg.DefaultUser != "root" && cfg.DefaultUser != "sandbox" {
		return fmt.Errorf("SANDBOX_DEFAULT_USER must be root or sandbox")
	}
	if cfg.MaxSandboxes <= 0 {
		return fmt.Errorf("SANDBOX_MAX_SANDBOXES must be greater than 0")
	}

	limits := []struct {
		name          string
		value, maxVal int
	}{
		{"SANDBOX_DEFAULT_CPU_MILLIS", cfg.DefaultCPUMillis, cfg.MaxCPUMillis},
		{"SANDBOX_DEFAULT_MEMORY_MB", cfg.DefaultMemoryMB, cfg.MaxMemoryMB},
		{"SANDBOX_DEFAULT_STORAGE_MB", cfg.DefaultStorageMB, cfg.MaxStorageMB},
		{"SANDBOX_DEFAULT_PIDS", cfg.DefaultPids, cfg.MaxPids},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("%s must be greater than 0", limit.name)
		}
		if limit.value > limit.maxVal {
			return fmt.Errorf("%s (%d) must not exceed its maximum (%d)", limit.name, limit.value, limit.maxVal)
		}
	}

	if cfg.DefaultTTL <= 0 || cfg.DefaultTTL > cfg.MaxTTL {
		return fmt.Errorf("SANDBOX_DEFAULT_TTL must be greater than 0 and at most SANDBOX_MAX_TTL")
	}
	if cfg.IdleTimeout < 0 {
		return fmt.Errorf("SANDBOX_IDLE_TIMEOUT must not be negative")
	}
	if cfg.ReaperInterval <= 0 {
		return fmt.Errorf("SANDBOX_REAPER_INTERVAL must be greater than 0")
	}
	if cfg.TombstoneTTL < 0 {
		return fmt.Errorf("SANDBOX_TOMBSTONE_TTL must not be negative")
	}
	if cfg.DestroyTimeout <= 0 {
		return fmt.Errorf("SANDBOX_DESTROY_TIMEOUT must be greater than 0")
	}

	if cfg.UIDMapBase <= 0 {
		return fmt.Errorf("SANDBOX_UID_MAP_BASE must be greater than 0 so sandbox root is not host root")
	}
	if cfg.UIDMapSize < 1024 {
		return fmt.Errorf("SANDBOX_UID_MAP_SIZE must be at least 1024")
	}
	if cfg.ExecUID <= 0 || cfg.ExecUID >= cfg.UIDMapSize || cfg.ExecGID <= 0 || cfg.ExecGID >= cfg.UIDMapSize {
		return fmt.Errorf("SANDBOX_EXEC_UID/GID must be non-root and inside the mapped id range")
	}
	return nil
}

func validateExecConfig(cfg ExecConfig, http HTTPConfig) error {
	if cfg.DefaultTimeout <= 0 || cfg.DefaultTimeout > cfg.MaxTimeout {
		return fmt.Errorf("SANDBOX_EXEC_DEFAULT_TIMEOUT must be greater than 0 and at most SANDBOX_EXEC_MAX_TIMEOUT")
	}
	// A command must finish (or be killed) before the HTTP write deadline,
	// otherwise the client sees a dropped connection instead of timed_out=true.
	if cfg.MaxTimeout >= http.WriteTimeout {
		return fmt.Errorf("SANDBOX_EXEC_MAX_TIMEOUT (%s) must be lower than HTTP_WRITE_TIMEOUT (%s)", cfg.MaxTimeout, http.WriteTimeout)
	}
	if cfg.MaxTimeout >= http.StreamWriteTimeout {
		return fmt.Errorf("SANDBOX_EXEC_MAX_TIMEOUT (%s) must be lower than HTTP_STREAM_WRITE_TIMEOUT (%s)", cfg.MaxTimeout, http.StreamWriteTimeout)
	}
	if cfg.MaxOutputBytes <= 0 {
		return fmt.Errorf("SANDBOX_EXEC_MAX_OUTPUT_BYTES must be greater than 0")
	}
	if cfg.MaxConcurrent <= 0 {
		return fmt.Errorf("SANDBOX_EXEC_MAX_CONCURRENT must be greater than 0")
	}
	if cfg.MaxSessions <= 0 {
		return fmt.Errorf("SANDBOX_EXEC_MAX_SESSIONS must be greater than 0")
	}
	if cfg.MaxTerminals <= 0 {
		return fmt.Errorf("SANDBOX_EXEC_MAX_TERMINALS must be greater than 0")
	}
	if cfg.TerminalDetachTimeout <= 0 {
		return fmt.Errorf("SANDBOX_TERMINAL_DETACH_TIMEOUT must be greater than 0")
	}
	if cfg.FileMaxBytes <= 0 {
		return fmt.Errorf("SANDBOX_FILE_MAX_BYTES must be greater than 0")
	}
	return nil
}

func validateNetworkConfig(cfg NetworkConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.BridgeName) == "" || len(cfg.BridgeName) > 15 {
		return fmt.Errorf("SANDBOX_NETWORK_BRIDGE must be 1-15 characters")
	}
	_, network, err := net.ParseCIDR(cfg.CIDR)
	if err != nil {
		return fmt.Errorf("SANDBOX_NETWORK_CIDR is invalid: %w", err)
	}
	if network.IP.To4() == nil {
		return fmt.Errorf("SANDBOX_NETWORK_CIDR must be an IPv4 network")
	}
	if ones, _ := network.Mask.Size(); ones > 28 {
		return fmt.Errorf("SANDBOX_NETWORK_CIDR must be /28 or larger")
	}
	for _, server := range cfg.DNS {
		if net.ParseIP(server) == nil {
			return fmt.Errorf("SANDBOX_NETWORK_DNS entry %q is not an IP address", server)
		}
	}
	for _, cidr := range cfg.EgressDeny {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("SANDBOX_NETWORK_EGRESS_DENY entry %q is invalid: %w", cidr, err)
		}
	}
	return nil
}
