package presentation

import "time"

// SandboxChangeResponse is one filesystem entry the sandbox's workload
// created, wrote to, or removed since it started.
type SandboxChangeResponse struct {
	Path string `json:"path" example:"/root/app.py"`
	// Kind is added for a path the image did not have, modified for one it
	// had that was then written to, deleted for one the sandbox removed, and
	// replaced for a directory emptied of everything the image put in it.
	Kind string `json:"kind" example:"added" enums:"added,modified,deleted,replaced"`
	// Type is file, dir, symlink or special. A deletion has none: the entry
	// is gone, and only the fact that it went is recorded.
	Type string `json:"type,omitempty" example:"file"`
	Size int64  `json:"size" example:"128"`
	Mode string `json:"mode,omitempty" example:"0644"`
	// ModifiedAt is absent for a deletion, for the same reason as Type.
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
	// Seeded marks a path the daemon writes into every sandbox: its hostname,
	// hosts and resolv.conf, the sandbox account, and the directories mounts
	// go on. It reports that the daemon writes there, not that the workload
	// left it alone, so hide these only when looking for the workload's work.
	Seeded bool `json:"seeded" example:"false"`
}

// SandboxChangesResponse is everything the workload did to the filesystem it
// was given, read from the overlay's writable layer.
type SandboxChangesResponse struct {
	Entries []SandboxChangeResponse `json:"entries"`
	// Truncated reports that the sandbox changed more paths than the daemon
	// is willing to list, so what follows is a prefix and not the whole set.
	Truncated bool `json:"truncated" example:"false"`
}

type SandboxChangesEnvelopeDoc = EnvelopeDoc[SandboxChangesResponse]

// PromoteSnapshotRequest names the base image a snapshot becomes.
type PromoteSnapshotRequest struct {
	// Image must match [a-z0-9][a-z0-9._-]{0,63} and must not already exist.
	Image string `json:"image" validate:"required,max=64" example:"python-ready"`
}
