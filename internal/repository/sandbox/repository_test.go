package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/model"
)

func TestCreateEnforcesActiveLimit(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	ctx := context.Background()
	if err := repo.Create(ctx, &model.Sandbox{ID: "a"}, 1); err != nil {
		t.Fatalf("Create(a) error = %v", err)
	}
	if err := repo.Create(ctx, &model.Sandbox{ID: "b"}, 1); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("Create(b) error = %v, want ErrLimitReached", err)
	}

	stoppedAt := time.Now()
	if _, err := repo.Update(ctx, "a", func(sb *model.Sandbox) error {
		sb.StoppedAt = &stoppedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &model.Sandbox{ID: "b"}, 1); err != nil {
		t.Fatalf("Create(b) after stop error = %v; stopped sandboxes must not count", err)
	}
}

func TestReturnedRecordsAreCopies(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	ctx := context.Background()
	if err := repo.Create(ctx, &model.Sandbox{ID: "a", Labels: map[string]string{"k": "v"}}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	got.Labels["k"] = "mutated"

	again, _ := repo.Get(ctx, "a")
	if again.Labels["k"] != "v" {
		t.Fatalf("stored record was mutated through a returned copy: %v", again.Labels)
	}
}

func TestUpdateErrorLeavesRecordUnchanged(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	ctx := context.Background()
	_ = repo.Create(ctx, &model.Sandbox{ID: "a", State: "running"}, 0)
	sentinel := errors.New("abort")
	if _, err := repo.Update(ctx, "a", func(sb *model.Sandbox) error {
		sb.State = "changed"
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update() error = %v", err)
	}
	got, _ := repo.Get(ctx, "a")
	if got.State != "running" {
		t.Fatalf("State = %q after aborted update", got.State)
	}
}

func TestListFiltersAndPurge(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	_ = repo.Create(ctx, &model.Sandbox{ID: "a", State: "running", Labels: map[string]string{"team": "x"}, CreatedAt: old}, 0)
	_ = repo.Create(ctx, &model.Sandbox{ID: "b", State: "stopped", StoppedAt: &old, CreatedAt: old.Add(time.Minute)}, 0)

	running, _ := repo.List(ctx, Filter{State: "running"})
	if len(running) != 1 || running[0].ID != "a" {
		t.Fatalf("List(state) = %+v", running)
	}
	labelled, _ := repo.List(ctx, Filter{LabelKey: "team", LabelValue: "x"})
	if len(labelled) != 1 || labelled[0].ID != "a" {
		t.Fatalf("List(label) = %+v", labelled)
	}
	if purged := repo.PurgeStopped(ctx, time.Now()); purged != 1 {
		t.Fatalf("PurgeStopped() = %d, want 1", purged)
	}
	if _, err := repo.Get(ctx, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(b) after purge error = %v", err)
	}
}

func TestSessionsAreScopedToSandbox(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	ctx := context.Background()
	if err := repo.CreateSession(ctx, &model.Session{ID: "s", SandboxID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateSession() for missing sandbox error = %v", err)
	}
	_ = repo.Create(ctx, &model.Sandbox{ID: "a"}, 0)
	_ = repo.Create(ctx, &model.Sandbox{ID: "b"}, 0)
	if err := repo.CreateSession(ctx, &model.Session{ID: "s", SandboxID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSession(ctx, "b", "s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession() across sandboxes error = %v", err)
	}
	if err := repo.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if sessions, _ := repo.ListSessions(ctx, "a"); len(sessions) != 0 {
		t.Fatalf("sessions survived sandbox delete: %+v", sessions)
	}
}
