package sandbox

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func nextSnapshotTime(now time.Time, interval int) time.Time {
	if interval == 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(interval) * time.Second)
}

func (s *service) validateSnapshotPolicy(policy *model.SnapshotPolicy) error {
	if policy == nil {
		return nil
	}
	if err := validation.Struct(policy); err != nil {
		return appctx.ErrLimitExceeded("backup interval must be 60–86400 seconds and retention 1–100")
	}
	if policy.IntervalSeconds > 0 || policy.BeforeExpiry {
		s.snapshotStoreMu.RLock()
		defer s.snapshotStoreMu.RUnlock()
		return s.snapshotEnabled()
	}
	return nil
}

func (s *service) SetSnapshotPolicy(ctx context.Context, id string, policy model.SnapshotPolicy) (*presentation.SandboxResponse, error) {
	if err := s.validateSnapshotPolicy(&policy); err != nil {
		return nil, err
	}
	if policy.Retain == 0 {
		policy.Retain = 5
	}
	sb, err := s.repo.Update(ctx, id, func(sb *model.Sandbox) error {
		if sb.State != consts.SandboxStateRunning {
			return appctx.ErrSandboxNotRunning()
		}
		if sb.SnapshotInProgress {
			return appctx.NewHTTPError(http.StatusConflict, "wait for the current backup before changing its policy").WithErrorCode(appctx.CodeSnapshotNotReady)
		}
		sb.SnapshotPolicy, sb.NextSnapshotAt, sb.LastSnapshotError = policy, nextSnapshotTime(s.now(), policy.IntervalSeconds), ""
		return nil
	})
	if err != nil {
		return nil, mapError(err)
	}
	return toSandboxResponse(sb), nil
}

// Automatic backups never overlap. Expiry backups run before TTL or idle
// teardown, with at most one snapshot-timeout of grace for unavailable storage.
// Failed final captures retain their filesystem for retry during that grace.
func (s *service) automaticSnapshot(ctx context.Context, sb *model.Sandbox, now, expiry time.Time) bool {
	if sb.SnapshotInProgress || sb.State == consts.SandboxStateSnapshotting {
		return true
	}
	final := !expiry.IsZero() && !now.Before(expiry)
	if final && sb.SnapshotPolicy.BeforeExpiry && now.Before(expiry.Add(s.snapshotCfg.Timeout)) {
		if !sb.ExpiryBackupAttempted || !now.Before(sb.NextSnapshotAt) {
			s.startAutomaticSnapshot(ctx, sb, false, "expiry")
		}
		return true
	}
	if !final && sb.State == consts.SandboxStateRunning && sb.SnapshotPolicy.IntervalSeconds > 0 && !now.Before(sb.NextSnapshotAt) {
		s.startAutomaticSnapshot(ctx, sb, true, "scheduled")
		return true
	}
	return false
}

func (s *service) startAutomaticSnapshot(ctx context.Context, sb *model.Sandbox, keepRunning bool, trigger string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.CreateSnapshot(ctx, sb.ID, presentation.CreateSnapshotRequest{KeepRunning: keepRunning, Trigger: trigger}); err != nil {
		_, _ = s.repo.Update(ctx, sb.ID, func(record *model.Sandbox) error {
			// A racing manual snapshot owns these fields until it finishes.
			if !record.SnapshotInProgress {
				record.LastSnapshotError = "automatic backup could not start; check cloud storage and available backup slots"
				record.NextSnapshotAt = s.now().Add(time.Minute)
				if trigger == "expiry" {
					record.ExpiryBackupAttempted = true
				}
			}
			return nil
		})
	}
}

func (s *service) pruneAutomaticSnapshots(ctx context.Context, current snapshot.Manifest) {
	if current.Trigger != "scheduled" && current.Trigger != "expiry" {
		return
	}
	retain := current.Config.SnapshotPolicy.Retain
	if retain <= 0 {
		retain = 5
	}
	items, err := s.snapshots.List(ctx)
	if err != nil {
		return
	}
	var candidates []snapshot.Manifest
	for _, item := range items {
		if item.SandboxID == current.SandboxID && item.Status == snapshot.Ready && (item.Trigger == "scheduled" || item.Trigger == "expiry") {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ID == candidates[j].ID {
			return false
		}
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			if candidates[i].ID == current.ID {
				return true
			}
			if candidates[j].ID == current.ID {
				return false
			}
			return candidates[i].ID > candidates[j].ID
		}
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})
	for i, item := range candidates {
		if i >= retain && item.ID != current.ID {
			_ = s.snapshots.Delete(ctx, item.ID)
		}
	}
}
