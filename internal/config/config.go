package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	App       AppConfig
	HTTP      HTTPConfig
	Telemetry TelemetryConfig
	Sandbox   SandboxConfig
	Exec      ExecConfig
	Network   NetworkConfig
	Snapshot  SnapshotConfig
	Preview   PreviewConfig
}

// PreviewConfig uses a separate listener and a unique subdomain for every session,
// keeping untrusted application JavaScript away from the console's origin.
type PreviewConfig struct {
	Enabled     bool
	Address     string
	BaseURL     string
	TTL         time.Duration
	MaxSessions int
}

// SnapshotConfig configures full filesystem backups in S3-compatible storage.
// Credentials come from the AWS SDK's daemon-side credential chain.
type SnapshotConfig struct {
	Enabled       bool
	Bucket        string
	Region        string
	Endpoint      string
	Prefix        string
	PathStyle     bool
	TempDir       string
	Timeout       time.Duration
	MaxBytes      int64
	MaxConcurrent int
	SettingsDir   string
}

type AppConfig struct {
	Name     string
	Env      string
	LogLevel string
	// APIKeys are the bearer tokens accepted on /api. Every key is equivalent;
	// multiple keys exist so they can be rotated without downtime.
	APIKeys     []string
	BaseURL     string
	DocsEnabled bool
	// UIEnabled serves the web console at /ui/.
	UIEnabled   bool
	CORSOrigins []string
}

type HTTPConfig struct {
	Host               string
	Port               int
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	StreamWriteTimeout time.Duration
	// BodyLimit caps request bodies; file uploads are additionally capped by
	// ExecConfig.FileMaxBytes.
	BodyLimit int
}

type TelemetryConfig struct {
	Enabled        bool
	MetricsEnabled bool
	Endpoint       string
	Headers        map[string]string
	Insecure       bool
	SampleRate     float64
	Protocol       string `validate:"omitempty,oneof=grpc http"`
}

// SandboxConfig tunes the sandbox engine: where state lives, how sandboxes map
// onto host identities, and the default/maximum resources a caller may request.
type SandboxConfig struct {
	// StateDir holds per-sandbox runtime directories (rootfs mounts, agent sockets).
	StateDir string
	// ImagesDir holds the uid-shifted base root filesystems, one directory per image.
	ImagesDir    string
	DefaultImage string
	// CgroupRoot is the delegated cgroup v2 directory the engine owns. Empty
	// means "the cgroup this process was started in" (systemd Delegate=yes or a
	// container with a private cgroup namespace).
	CgroupRoot   string
	MaxSandboxes int

	DefaultCPUMillis int
	MaxCPUMillis     int
	DefaultMemoryMB  int
	MaxMemoryMB      int
	DefaultStorageMB int
	MaxStorageMB     int
	DefaultPids      int
	MaxPids          int

	DefaultTTL     time.Duration
	MaxTTL         time.Duration
	IdleTimeout    time.Duration
	ReaperInterval time.Duration
	// TombstoneTTL is how long stopped/failed sandboxes stay visible via GET.
	TombstoneTTL time.Duration

	// UIDMapBase is the first host uid/gid that sandbox uid/gid 0 maps to;
	// UIDMapSize ids are mapped in total.
	UIDMapBase int
	UIDMapSize int
	// ExecUID/ExecGID is the in-sandbox identity commands run as.
	ExecUID int
	ExecGID int

	SeccompEnabled bool
	// StorageDriver keeps sandboxes' writable layers on disk under a quota
	// ("disk") or in memory ("tmpfs").
	StorageDriver string
	// StorageFS is the filesystem inside disk storage images: "ext4",
	// "btrfs", or empty for the first one the host supports.
	StorageFS string
	// DefaultUser runs commands, sessions, and terminals that name no user:
	// "root" or "sandbox".
	DefaultUser string
	// DestroyTimeout bounds teardown of a single sandbox.
	DestroyTimeout time.Duration
}

type ExecConfig struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int
	MaxConcurrent  int
	MaxSessions    int
	MaxTerminals   int
	// TerminalDetachTimeout is how long a terminal's shell keeps running
	// with no client attached, so a reloaded page can reattach to it.
	TerminalDetachTimeout time.Duration
	FileMaxBytes          int64
}

type NetworkConfig struct {
	// Enabled allows the egress network mode. When false only "none" is accepted.
	Enabled    bool
	BridgeName string
	CIDR       string
	DNS        []string
	// EgressDeny lists extra CIDRs blocked in addition to the built-in
	// link-local, private, CGNAT, loopback, and host-address ranges.
	EgressDeny []string
	IPForward  bool
}

func Load() (Config, error) {
	_ = godotenv.Load()
	appEnv := getEnv("APP_ENV", "development")
	stateDir := getEnv("SANDBOX_STATE_DIR", "/var/lib/sandboxd")
	cfg := Config{
		Preview: PreviewConfig{
			Enabled:     getEnvAsBool("PREVIEW_ENABLED", false),
			Address:     getEnv("PREVIEW_ADDRESS", "0.0.0.0:8081"),
			BaseURL:     getEnv("PREVIEW_BASE_URL", "http://localhost:8081"),
			TTL:         getEnvAsDuration("PREVIEW_TTL", time.Hour),
			MaxSessions: getEnvAsInt("PREVIEW_MAX_SESSIONS", 128),
		},
		App: AppConfig{
			Name:        getEnv("APP_NAME", "sandboxd"),
			Env:         appEnv,
			LogLevel:    getEnv("LOG_LEVEL", ""),
			APIKeys:     getEnvAsList("APP_API_KEYS", nil),
			BaseURL:     getEnv("APP_BASE_URL", "http://localhost:8080"),
			DocsEnabled: getEnvAsBool("APP_DOCS_ENABLED", defaultDocsEnabled(appEnv)),
			UIEnabled:   getEnvAsBool("UI_ENABLED", false),
			CORSOrigins: getEnvAsList("APP_CORS_ALLOWED_ORIGINS", nil),
		},
		HTTP: HTTPConfig{
			Host:               getEnv("HTTP_HOST", "0.0.0.0"),
			Port:               getEnvAsInt("HTTP_PORT", 8080),
			ReadTimeout:        getEnvAsDuration("HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:       getEnvAsDuration("HTTP_WRITE_TIMEOUT", 15*time.Minute),
			StreamWriteTimeout: getEnvAsDuration("HTTP_STREAM_WRITE_TIMEOUT", 30*time.Minute),
			BodyLimit:          getEnvAsInt("HTTP_BODY_LIMIT", 4<<20),
		},
		Telemetry: TelemetryConfig{
			Enabled:        getEnvAsBool("TELEMETRY_ENABLED", false),
			MetricsEnabled: getEnvAsBool("TELEMETRY_METRICS_ENABLED", true),
			Endpoint:       getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			Headers:        getEnvAsMap("OTEL_EXPORTER_OTLP_HEADERS", map[string]string{}),
			Insecure:       getEnvAsBool("OTEL_EXPORTER_OTLP_INSECURE", false),
			SampleRate:     getEnvAsFloat64("OTEL_TRACES_SAMPLER_RATIO", 1.0),
			Protocol:       getEnv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"),
		},
		Sandbox: SandboxConfig{
			StateDir:     stateDir,
			ImagesDir:    getEnv("SANDBOX_IMAGES_DIR", stateDir+"/images"),
			DefaultImage: getEnv("SANDBOX_DEFAULT_IMAGE", "alpine"),
			CgroupRoot:   getEnv("SANDBOX_CGROUP_ROOT", ""),
			MaxSandboxes: getEnvAsInt("SANDBOX_MAX_SANDBOXES", 64),

			DefaultCPUMillis: getEnvAsInt("SANDBOX_DEFAULT_CPU_MILLIS", 1000),
			MaxCPUMillis:     getEnvAsInt("SANDBOX_MAX_CPU_MILLIS", 4000),
			DefaultMemoryMB:  getEnvAsInt("SANDBOX_DEFAULT_MEMORY_MB", 512),
			MaxMemoryMB:      getEnvAsInt("SANDBOX_MAX_MEMORY_MB", 4096),
			DefaultStorageMB: getEnvAsInt("SANDBOX_DEFAULT_STORAGE_MB", 512),
			MaxStorageMB:     getEnvAsInt("SANDBOX_MAX_STORAGE_MB", 4096),
			DefaultPids:      getEnvAsInt("SANDBOX_DEFAULT_PIDS", 256),
			MaxPids:          getEnvAsInt("SANDBOX_MAX_PIDS", 4096),

			DefaultTTL:     getEnvAsDuration("SANDBOX_DEFAULT_TTL", 30*time.Minute),
			MaxTTL:         getEnvAsDuration("SANDBOX_MAX_TTL", 24*time.Hour),
			IdleTimeout:    getEnvAsDuration("SANDBOX_IDLE_TIMEOUT", 10*time.Minute),
			ReaperInterval: getEnvAsDuration("SANDBOX_REAPER_INTERVAL", 15*time.Second),
			TombstoneTTL:   getEnvAsDuration("SANDBOX_TOMBSTONE_TTL", 10*time.Minute),

			UIDMapBase: getEnvAsInt("SANDBOX_UID_MAP_BASE", 100000),
			UIDMapSize: getEnvAsInt("SANDBOX_UID_MAP_SIZE", 65536),
			ExecUID:    getEnvAsInt("SANDBOX_EXEC_UID", 1000),
			ExecGID:    getEnvAsInt("SANDBOX_EXEC_GID", 1000),

			SeccompEnabled: getEnvAsBool("SANDBOX_SECCOMP_ENABLED", true),
			DefaultUser:    getEnv("SANDBOX_DEFAULT_USER", "root"),
			StorageDriver:  getEnv("SANDBOX_STORAGE_DRIVER", "disk"),
			StorageFS:      getEnv("SANDBOX_STORAGE_FS", ""),
			DestroyTimeout: getEnvAsDuration("SANDBOX_DESTROY_TIMEOUT", 10*time.Second),
		},
		Exec: ExecConfig{
			DefaultTimeout:        getEnvAsDuration("SANDBOX_EXEC_DEFAULT_TIMEOUT", time.Minute),
			MaxTimeout:            getEnvAsDuration("SANDBOX_EXEC_MAX_TIMEOUT", 10*time.Minute),
			MaxOutputBytes:        getEnvAsInt("SANDBOX_EXEC_MAX_OUTPUT_BYTES", 1<<20),
			MaxConcurrent:         getEnvAsInt("SANDBOX_EXEC_MAX_CONCURRENT", 8),
			MaxSessions:           getEnvAsInt("SANDBOX_EXEC_MAX_SESSIONS", 8),
			MaxTerminals:          getEnvAsInt("SANDBOX_EXEC_MAX_TERMINALS", 4),
			TerminalDetachTimeout: getEnvAsDuration("SANDBOX_TERMINAL_DETACH_TIMEOUT", 10*time.Minute),
			FileMaxBytes:          int64(getEnvAsInt("SANDBOX_FILE_MAX_BYTES", 100<<20)),
		},
		Network: NetworkConfig{
			Enabled:    getEnvAsBool("SANDBOX_NETWORK_ENABLED", true),
			BridgeName: getEnv("SANDBOX_NETWORK_BRIDGE", "sbx0"),
			CIDR:       getEnv("SANDBOX_NETWORK_CIDR", "10.88.0.0/16"),
			DNS:        getEnvAsList("SANDBOX_NETWORK_DNS", []string{"1.1.1.1", "8.8.8.8"}),
			EgressDeny: getEnvAsList("SANDBOX_NETWORK_EGRESS_DENY", nil),
			IPForward:  getEnvAsBool("SANDBOX_NETWORK_IP_FORWARD", true),
		},
		Snapshot: SnapshotConfig{
			Enabled:       getEnvAsBool("SNAPSHOT_ENABLED", false),
			Bucket:        getEnv("SNAPSHOT_S3_BUCKET", ""),
			Region:        getEnv("SNAPSHOT_S3_REGION", "us-east-1"),
			Endpoint:      getEnv("SNAPSHOT_S3_ENDPOINT", ""),
			Prefix:        getEnv("SNAPSHOT_S3_PREFIX", "sandboxd/snapshots"),
			PathStyle:     getEnvAsBool("SNAPSHOT_S3_PATH_STYLE", false),
			TempDir:       getEnv("SNAPSHOT_TEMP_DIR", stateDir+"/snapshot-tmp"),
			Timeout:       getEnvAsDuration("SNAPSHOT_TIMEOUT", 30*time.Minute),
			MaxBytes:      int64(getEnvAsInt("SNAPSHOT_MAX_BYTES", 4<<30)),
			MaxConcurrent: getEnvAsInt("SNAPSHOT_MAX_CONCURRENT", 2),
			SettingsDir:   getEnv("SNAPSHOT_SETTINGS_DIR", stateDir+"/cloud-storage"),
		},
	}

	cfg.Telemetry.Protocol = strings.ToLower(strings.TrimSpace(cfg.Telemetry.Protocol))
	if cfg.Telemetry.Enabled && cfg.Telemetry.Endpoint == "" {
		return Config{}, fmt.Errorf("telemetry enabled but OTEL_EXPORTER_OTLP_ENDPOINT is empty")
	}

	if err := validateStaticConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateAppConfig(cfg.App); err != nil {
		return Config{}, err
	}
	if err := validateSandboxConfig(cfg.Sandbox); err != nil {
		return Config{}, err
	}
	if err := validateExecConfig(cfg.Exec, cfg.HTTP); err != nil {
		return Config{}, err
	}
	if err := validateNetworkConfig(cfg.Network); err != nil {
		return Config{}, err
	}
	if err := validateSnapshotConfig(cfg.Snapshot); err != nil {
		return Config{}, err
	}
	if err := validatePreviewConfig(cfg.Preview); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
