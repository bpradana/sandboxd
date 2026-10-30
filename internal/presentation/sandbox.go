package presentation

import (
	"time"

	"github.com/bpradana/sandboxd/internal/model"
)

type CreateSandboxRequest struct {
	SnapshotPolicy *model.SnapshotPolicy `json:"snapshot_policy,omitempty" validate:"omitempty"`
	// Image is the base root filesystem; defaults to SANDBOX_DEFAULT_IMAGE.
	Image string `json:"image" validate:"excluded_with=SnapshotID,omitempty,max=64" example:"alpine"`
	// SnapshotID restores a full filesystem backup; mutually exclusive with image.
	SnapshotID string `json:"snapshot_id" validate:"omitempty,max=64"`
	// CPUMillis is the CPU quota in millicores (1000 = one full core).
	CPUMillis int `json:"cpu_millis" validate:"omitempty,gte=10" example:"1000"`
	// MemoryMB is the hard memory limit; exceeding it OOM-kills the sandbox's processes.
	MemoryMB int `json:"memory_mb" validate:"omitempty,gte=16" example:"512"`
	// StorageMB caps the writable layer; writes beyond it fail with ENOSPC.
	StorageMB int `json:"storage_mb" validate:"omitempty,gte=16" example:"512"`
	// PidsMax caps the number of processes and threads.
	PidsMax int `json:"pids_max" validate:"omitempty,gte=8" example:"256"`
	// Network is none (loopback only) or egress (outbound internet, private ranges blocked).
	Network string `json:"network" validate:"omitempty,oneof=none egress" example:"none"`
	// TTLSeconds is the lifetime after which the sandbox is destroyed.
	TTLSeconds int `json:"ttl_seconds" validate:"omitempty,gte=1" example:"1800"`
	// IdleTimeoutSeconds destroys the sandbox after this long without exec or file activity; 0 uses the server default.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds" validate:"omitempty,gte=1" example:"600"`
	// Env is applied to every command run in the sandbox.
	Env map[string]string `json:"env" validate:"omitempty,max=128,dive,keys,notblank,max=256,endkeys,max=4096"`
	// Labels are opaque caller metadata usable as list filters.
	Labels map[string]string `json:"labels" validate:"omitempty,max=32,dive,keys,notblank,max=63,endkeys,max=256"`
}

type ListSandboxesRequest struct {
	// State filters by lifecycle state.
	State string `query:"state" validate:"omitempty,oneof=creating running snapshotting stopping stopped failed"`
	// Label filters by one key=value label.
	Label string `query:"label" validate:"omitempty,max=320"`
}

type KeepaliveRequest struct {
	// TTLSeconds is the new remaining lifetime from now; defaults to SANDBOX_DEFAULT_TTL, capped at SANDBOX_MAX_TTL from creation.
	TTLSeconds int `json:"ttl_seconds" validate:"omitempty,gte=1" example:"1800"`
}

type LimitsResponse struct {
	CPUMillis int `json:"cpu_millis" example:"1000"`
	MemoryMB  int `json:"memory_mb" example:"512"`
	StorageMB int `json:"storage_mb" example:"512"`
	PidsMax   int `json:"pids_max" example:"256"`
}

type SandboxResponse struct {
	SnapshotPolicy     model.SnapshotPolicy `json:"snapshot_policy"`
	SnapshotInProgress bool                 `json:"snapshot_in_progress"`
	NextSnapshotAt     *time.Time           `json:"next_snapshot_at,omitempty"`
	LastSnapshotAt     *time.Time           `json:"last_snapshot_at,omitempty"`
	LastSnapshotError  string               `json:"last_snapshot_error,omitempty"`
	// Node is the daemon this sandbox lives on. A sandbox is kernel state on
	// one machine; anything routing across several needs to know which.
	Node               string                `json:"node" example:"sandboxd-1"`
	ID                 string                `json:"id" example:"sbx_01jbq3w4k7m2x9z8a1b2c3d4e5"`
	Image              string                `json:"image" example:"alpine"`
	State              string                `json:"state" example:"running"`
	Network            string                `json:"network" example:"none"`
	IPAddress          string                `json:"ip_address,omitempty" example:"10.88.0.2"`
	Limits             LimitsResponse        `json:"limits"`
	Env                map[string]string     `json:"env,omitempty"`
	Labels             map[string]string     `json:"labels,omitempty"`
	IdleTimeoutSeconds int                   `json:"idle_timeout_seconds" example:"600"`
	CreatedAt          time.Time             `json:"created_at"`
	ExpiresAt          time.Time             `json:"expires_at"`
	LastActivityAt     time.Time             `json:"last_activity_at"`
	StoppedAt          *time.Time            `json:"stopped_at,omitempty"`
	StopReason         string                `json:"stop_reason,omitempty" example:"expired"`
	FailureReason      string                `json:"failure_reason,omitempty"`
	Stats              *SandboxStatsResponse `json:"stats,omitempty"`
}

type SandboxStatsResponse struct {
	// MemoryBytes is current memory usage, including tmpfs-backed storage.
	MemoryBytes      int64 `json:"memory_bytes" example:"10485760"`
	MemoryPeakBytes  int64 `json:"memory_peak_bytes" example:"20971520"`
	MemoryLimitBytes int64 `json:"memory_limit_bytes" example:"536870912"`
	CPUUsageMicros   int64 `json:"cpu_usage_micros" example:"152000"`
	PidsCurrent      int64 `json:"pids_current" example:"3"`
	PidsLimit        int64 `json:"pids_limit" example:"256"`
	// OOMKills counts processes killed for exceeding the memory limit.
	OOMKills          int64 `json:"oom_kills" example:"0"`
	StorageUsedBytes  int64 `json:"storage_used_bytes" example:"4096"`
	StorageLimitBytes int64 `json:"storage_limit_bytes" example:"536870912"`
}

type ImageResponse struct {
	Name      string    `json:"name" example:"alpine"`
	CreatedAt time.Time `json:"created_at"`
	// Local reports whether this host already holds the image. One that is
	// not local is still usable: the first sandbox asking for it downloads
	// and imports it, which takes about as long as restoring a snapshot.
	Local bool `json:"local" example:"true"`
	// Architecture is set for an image held in cloud storage, which can only
	// be materialized on a host of the same architecture.
	Architecture string `json:"architecture,omitempty" example:"arm64"`
}
