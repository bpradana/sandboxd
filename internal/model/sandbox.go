// Package model defines the in-memory domain records owned by the repository.
package model

import "time"

// Limits are the resources granted to one sandbox.
type Limits struct {
	CPUMillis int
	MemoryMB  int
	StorageMB int
	PidsMax   int
}

type SnapshotPolicy struct {
	IntervalSeconds int  `json:"interval_seconds" validate:"omitempty,min=60,max=86400"`
	BeforeExpiry    bool `json:"before_expiry"`
	Retain          int  `json:"retain" validate:"omitempty,min=1,max=100"`
}

// Sandbox is the API-visible record of one isolated environment. Kernel-level
// handles (cgroup, mounts, init process) are owned by the engine, keyed by ID.
type Sandbox struct {
	ID                    string
	Image                 string
	State                 string
	Network               string
	IPAddress             string
	Limits                Limits
	Env                   map[string]string
	Labels                map[string]string
	IdleTimeout           time.Duration
	CreatedAt             time.Time
	ExpiresAt             time.Time
	LastActivityAt        time.Time
	StoppedAt             *time.Time
	StopReason            string
	FailureReason         string
	SnapshotPolicy        SnapshotPolicy
	SnapshotInProgress    bool
	NextSnapshotAt        time.Time
	LastSnapshotAt        time.Time
	LastSnapshotError     string
	ExpiryBackupAttempted bool
}

// Clone returns a deep copy so callers never share maps with the repository.
func (s *Sandbox) Clone() *Sandbox {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Env = cloneMap(s.Env)
	clone.Labels = cloneMap(s.Labels)
	if s.StoppedAt != nil {
		stoppedAt := *s.StoppedAt
		clone.StoppedAt = &stoppedAt
	}
	return &clone
}

// Active reports whether the sandbox still holds kernel resources.
func (s *Sandbox) Active() bool {
	return s.StoppedAt == nil
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
