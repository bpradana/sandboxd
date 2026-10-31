package sandbox

import (
	"fmt"
	"os"
	"time"

	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
)

// toSandboxResponse is a method so every response carries the node the
// sandbox lives on: a sandbox is kernel state on exactly one machine, and a
// caller holding an ID needs to know which one.
func (s *service) toSandboxResponse(sb *model.Sandbox) *presentation.SandboxResponse {
	return &presentation.SandboxResponse{
		Node:           s.sandbox.NodeID,
		SnapshotPolicy: sb.SnapshotPolicy, SnapshotInProgress: sb.SnapshotInProgress,
		NextSnapshotAt: optionalTime(sb.NextSnapshotAt), LastSnapshotAt: optionalTime(sb.LastSnapshotAt), LastSnapshotError: sb.LastSnapshotError,
		ID:        sb.ID,
		Image:     sb.Image,
		State:     sb.State,
		Network:   sb.Network,
		IPAddress: sb.IPAddress,
		Limits: presentation.LimitsResponse{
			CPUMillis: sb.Limits.CPUMillis,
			MemoryMB:  sb.Limits.MemoryMB,
			StorageMB: sb.Limits.StorageMB,
			PidsMax:   sb.Limits.PidsMax,
		},
		Env:                sb.Env,
		Labels:             sb.Labels,
		IdleTimeoutSeconds: int(sb.IdleTimeout.Seconds()),
		CreatedAt:          sb.CreatedAt,
		ExpiresAt:          sb.ExpiresAt,
		LastActivityAt:     sb.LastActivityAt,
		StoppedAt:          sb.StoppedAt,
		StopReason:         sb.StopReason,
		FailureReason:      sb.FailureReason,
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func toStatsResponse(stats *engine.Stats) *presentation.SandboxStatsResponse {
	return &presentation.SandboxStatsResponse{
		MemoryBytes:       stats.MemoryBytes,
		MemoryPeakBytes:   stats.MemoryPeakBytes,
		MemoryLimitBytes:  stats.MemoryLimitBytes,
		CPUUsageMicros:    stats.CPUUsageMicros,
		PidsCurrent:       stats.PidsCurrent,
		PidsLimit:         stats.PidsLimit,
		OOMKills:          stats.OOMKills,
		StorageUsedBytes:  stats.StorageUsedBytes,
		StorageLimitBytes: stats.StorageLimitBytes,
	}
}

func toSessionResponse(session *model.Session) presentation.SessionResponse {
	return presentation.SessionResponse{
		ID:             session.ID,
		SandboxID:      session.SandboxID,
		Shell:          session.Shell,
		User:           session.User,
		State:          session.State,
		CreatedAt:      session.CreatedAt,
		LastActivityAt: session.LastActivityAt,
	}
}

func toFileInfoResponse(info *engine.FileInfo) *presentation.FileInfoResponse {
	return &presentation.FileInfoResponse{
		Path:       info.Path,
		Name:       info.Name,
		Type:       info.Type,
		Size:       info.Size,
		Mode:       formatMode(info.Mode),
		ModifiedAt: info.ModTime,
		LinkTarget: info.LinkTarget,
	}
}

// formatMode renders the permission bits in the familiar octal form,
// including setuid/setgid/sticky.
func formatMode(mode os.FileMode) string {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 0o1000
	}
	return fmt.Sprintf("%04o", bits)
}
