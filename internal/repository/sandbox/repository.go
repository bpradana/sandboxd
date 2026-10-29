// Package sandbox stores sandbox and session records in process memory. State
// is intentionally not persisted: kernel resources do not survive a restart
// either, and the engine reconciles leftovers on startup.
package sandbox

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bpradana/sandboxd/internal/model"
)

var (
	ErrNotFound      = errors.New("record not found")
	ErrAlreadyExists = errors.New("record already exists")
	ErrLimitReached  = errors.New("active sandbox limit reached")
)

type Filter struct {
	State      string
	LabelKey   string
	LabelValue string
}

type Repository interface {
	// Create inserts sb unless maxActive active sandboxes already exist.
	Create(ctx context.Context, sb *model.Sandbox, maxActive int) error
	Get(ctx context.Context, id string) (*model.Sandbox, error)
	List(ctx context.Context, filter Filter) ([]*model.Sandbox, error)
	// Update applies fn to the stored record atomically; an error from fn
	// aborts the update and is returned unchanged.
	Update(ctx context.Context, id string, fn func(sb *model.Sandbox) error) (*model.Sandbox, error)
	Delete(ctx context.Context, id string) error
	// PurgeStopped deletes inactive sandboxes stopped before cutoff and returns how many were removed.
	PurgeStopped(ctx context.Context, cutoff time.Time) int

	CreateSession(ctx context.Context, session *model.Session) error
	GetSession(ctx context.Context, sandboxID, id string) (*model.Session, error)
	ListSessions(ctx context.Context, sandboxID string) ([]*model.Session, error)
	UpdateSession(ctx context.Context, sandboxID, id string, fn func(session *model.Session) error) (*model.Session, error)
	DeleteSession(ctx context.Context, sandboxID, id string) error
}

type repository struct {
	mu        sync.RWMutex
	sandboxes map[string]*model.Sandbox
	sessions  map[string]map[string]*model.Session
}

func NewRepository() Repository {
	return &repository{
		sandboxes: make(map[string]*model.Sandbox),
		sessions:  make(map[string]map[string]*model.Session),
	}
}

func (r *repository) Create(_ context.Context, sb *model.Sandbox, maxActive int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.sandboxes[sb.ID]; exists {
		return ErrAlreadyExists
	}
	if maxActive > 0 {
		active := 0
		for _, existing := range r.sandboxes {
			if existing.Active() {
				active++
			}
		}
		if active >= maxActive {
			return ErrLimitReached
		}
	}
	r.sandboxes[sb.ID] = sb.Clone()
	return nil
}

func (r *repository) Get(_ context.Context, id string) (*model.Sandbox, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sb, ok := r.sandboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return sb.Clone(), nil
}

func (r *repository) List(_ context.Context, filter Filter) ([]*model.Sandbox, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*model.Sandbox, 0, len(r.sandboxes))
	for _, sb := range r.sandboxes {
		if filter.State != "" && sb.State != filter.State {
			continue
		}
		if filter.LabelKey != "" {
			if value, ok := sb.Labels[filter.LabelKey]; !ok || value != filter.LabelValue {
				continue
			}
		}
		result = append(result, sb.Clone())
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (r *repository) Update(_ context.Context, id string, fn func(sb *model.Sandbox) error) (*model.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.sandboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	working := stored.Clone()
	if err := fn(working); err != nil {
		return nil, err
	}
	r.sandboxes[id] = working
	return working.Clone(), nil
}

func (r *repository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.sandboxes[id]; !ok {
		return ErrNotFound
	}
	delete(r.sandboxes, id)
	delete(r.sessions, id)
	return nil
}

func (r *repository) PurgeStopped(_ context.Context, cutoff time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	purged := 0
	for id, sb := range r.sandboxes {
		if sb.StoppedAt != nil && sb.StoppedAt.Before(cutoff) {
			delete(r.sandboxes, id)
			delete(r.sessions, id)
			purged++
		}
	}
	return purged
}

func (r *repository) CreateSession(_ context.Context, session *model.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.sandboxes[session.SandboxID]; !ok {
		return ErrNotFound
	}
	bySandbox := r.sessions[session.SandboxID]
	if bySandbox == nil {
		bySandbox = make(map[string]*model.Session)
		r.sessions[session.SandboxID] = bySandbox
	}
	if _, exists := bySandbox[session.ID]; exists {
		return ErrAlreadyExists
	}
	bySandbox[session.ID] = session.Clone()
	return nil
}

func (r *repository) GetSession(_ context.Context, sandboxID, id string) (*model.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.sessions[sandboxID][id]
	if !ok {
		return nil, ErrNotFound
	}
	return session.Clone(), nil
}

func (r *repository) ListSessions(_ context.Context, sandboxID string) ([]*model.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bySandbox := r.sessions[sandboxID]
	result := make([]*model.Session, 0, len(bySandbox))
	for _, session := range bySandbox {
		result = append(result, session.Clone())
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (r *repository) UpdateSession(_ context.Context, sandboxID, id string, fn func(session *model.Session) error) (*model.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.sessions[sandboxID][id]
	if !ok {
		return nil, ErrNotFound
	}
	working := stored.Clone()
	if err := fn(working); err != nil {
		return nil, err
	}
	r.sessions[sandboxID][id] = working
	return working.Clone(), nil
}

func (r *repository) DeleteSession(_ context.Context, sandboxID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.sessions[sandboxID][id]; !ok {
		return ErrNotFound
	}
	delete(r.sessions[sandboxID], id)
	return nil
}
