// Package snapshot stores portable filesystem backups and their manifests.
package snapshot

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/bpradana/sandboxd/internal/model"
)

const (
	Version   = 1
	Capturing = "capturing"
	Uploading = "uploading"
	Ready     = "ready"
	Failed    = "failed"
)

var (
	ErrNotFound  = errors.New("snapshot not found")
	ErrInvalidID = errors.New("invalid snapshot id")
	ErrTooLarge  = errors.New("snapshot exceeds maximum allowed size")
	ErrCorrupt   = errors.New("snapshot checksum or size mismatch")
	idPattern    = regexp.MustCompile(`^snap-[a-f0-9]{24}$`)
)

func ValidID(id string) bool { return idPattern.MatchString(id) }

// Manifest travels with the archive, so restoring on another host needs no
// original base image. Env is deliberately omitted from public API responses.
type Manifest struct {
	Mode          string            `json:"mode,omitempty"`
	Trigger       string            `json:"trigger,omitempty"`
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	SandboxID     string            `json:"sandbox_id"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	DeadlineAt    time.Time         `json:"deadline_at"`
	SizeBytes     int64             `json:"size_bytes"`
	SHA256        string            `json:"sha256"`
	Architecture  string            `json:"architecture"`
	Labels        map[string]string `json:"labels,omitempty"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Config        SandboxConfig     `json:"config"`
}

type SandboxConfig struct {
	SnapshotPolicy     model.SnapshotPolicy `json:"snapshot_policy"`
	Image              string               `json:"image"`
	Network            string               `json:"network"`
	Limits             model.Limits         `json:"limits"`
	Env                map[string]string    `json:"env,omitempty"`
	Labels             map[string]string    `json:"labels,omitempty"`
	IdleTimeoutSeconds int                  `json:"idle_timeout_seconds"`
}

// EffectiveStatus makes an interrupted job observable after its host restarts.
func (m Manifest) EffectiveStatus(now time.Time) string {
	if (m.Status == Capturing || m.Status == Uploading) && now.After(m.DeadlineAt) {
		return Failed
	}
	return m.Status
}

// ImageRef makes a promoted base image usable on more than the one host that
// promoted it. The image is not stored again: the reference names the
// snapshot its files came from, which is already uploaded and verified, so a
// host that has never seen the image can build it from the same archive a
// restore would use.
type ImageRef struct {
	Name         string    `json:"name"`
	SnapshotID   string    `json:"snapshot_id"`
	Architecture string    `json:"architecture"`
	CreatedAt    time.Time `json:"created_at"`
	SizeBytes    int64     `json:"size_bytes"`
}

type Store interface {
	PutManifest(context.Context, Manifest) error
	GetManifest(context.Context, string) (Manifest, error)
	List(context.Context) ([]Manifest, error)
	Upload(context.Context, string, io.ReadSeeker, int64, string) error
	Download(context.Context, Manifest, io.Writer) error
	Delete(context.Context, string) error

	// PutImageRef records a promoted image so other hosts can find it.
	PutImageRef(context.Context, ImageRef) error
	GetImageRef(context.Context, string) (ImageRef, error)
	ListImageRefs(context.Context) ([]ImageRef, error)
	DeleteImageRef(context.Context, string) error
}

// Verifier can validate a stored archive against its local source without
// downloading it again. Stores without this capability use a full readback.
type Verifier interface {
	Verify(context.Context, Manifest, io.ReaderAt) error
}

// ContextReader checks cancellation between reads, including archive/file IO.
type ContextReader struct {
	Ctx    context.Context
	Reader io.Reader
}

func (r ContextReader) Read(p []byte) (int, error) {
	if err := r.Ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// LimitedWriter caps local disk use and archive expansion independently of
// compression ratios. Writes beyond the limit fail rather than truncate.
type LimitedWriter struct {
	Writer    io.Writer
	Remaining int64
}

func (w *LimitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, ErrTooLarge
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, err
}
