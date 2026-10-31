package sandbox

import (
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

// Phases a snapshot job moves through, in order. A backup captures the
// filesystem, uploads it and verifies what was stored; a restore downloads
// the archive and imports it into a new sandbox.
const (
	phaseCapturing   = "capturing"
	phaseUploading   = "uploading"
	phaseVerifying   = "verifying"
	phaseDownloading = "downloading"
	phaseImporting   = "importing"

	operationBackup  = "backup"
	operationRestore = "restore"
	// operationPromote downloads an archive and imports it as a base image,
	// so it runs a restore's two phases without building a sandbox.
	operationPromote = "promote"
)

// snapshotProgress is how far one running job has got. It lives in memory
// only: it is interesting while the job runs and meaningless once it ends,
// and persisting it would mean a cloud write per part. A job interrupted by a
// restart therefore reports no progress, which matches how its manifest is
// already read as failed once its deadline passes.
type snapshotProgress struct {
	operation string
	phase     string
	done      int64
	total     int64
}

// progressTracker holds the live progress of every running job, keyed by
// snapshot ID. Transfers write to it from part workers while the API reads it.
type progressTracker struct {
	mu   sync.RWMutex
	jobs map[string]snapshotProgress
}

func newProgressTracker() *progressTracker {
	return &progressTracker{jobs: map[string]snapshotProgress{}}
}

// enter records that a job has reached a phase. total is 0 when the size is
// not yet known, as during capture.
func (p *progressTracker) enter(id, operation, phase string, done, total int64) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[id] = snapshotProgress{operation: operation, phase: phase, done: done, total: total}
}

// advance moves the byte count of the phase the job is already in. A report
// arriving after the job moved on is dropped rather than rewinding the phase.
func (p *progressTracker) advance(id, phase string, done int64) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	job, ok := p.jobs[id]
	if !ok || job.phase != phase || done <= job.done {
		return
	}
	job.done = done
	p.jobs[id] = job
}

func (p *progressTracker) done(id string) {
	if p == nil || id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.jobs, id)
}

func (p *progressTracker) get(id string) (snapshotProgress, bool) {
	if p == nil {
		return snapshotProgress{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	job, ok := p.jobs[id]
	return job, ok
}

// reporter is the sink a transfer reports into while the job is in phase.
func (p *progressTracker) reporter(id, phase string) snapshot.Reporter {
	return func(transferred int64) { p.advance(id, phase, transferred) }
}

// list renders every running job, newest id order being irrelevant: callers
// match them to snapshots by ID.
func (p *progressTracker) list() []presentation.SnapshotJobResponse {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	jobs := make([]presentation.SnapshotJobResponse, 0, len(p.jobs))
	for id, job := range p.jobs {
		jobs = append(jobs, presentation.SnapshotJobResponse{
			SnapshotID: id,
			Progress: presentation.SnapshotProgressResponse{
				Operation: job.operation, Phase: job.phase,
				BytesDone: job.done, BytesTotal: job.total,
			},
		})
	}
	slices.SortFunc(jobs, func(a, b presentation.SnapshotJobResponse) int {
		return strings.Compare(a.SnapshotID, b.SnapshotID)
	})
	return jobs
}

// response renders a job for the API, or nil when nothing is running.
func (p *progressTracker) response(id string) *presentation.SnapshotProgressResponse {
	job, ok := p.get(id)
	if !ok {
		return nil
	}
	return &presentation.SnapshotProgressResponse{
		Operation: job.operation, Phase: job.phase,
		BytesDone: job.done, BytesTotal: job.total,
	}
}

// progressStep is how much must be written between reports. A capture writes
// in small chunks, and the console shows megabytes, so reporting every one
// keeps the tracker's lock out of the archive's write path.
const progressStep = 1 << 20

// countingWriter reports what a capture has written so far. The archive's
// final size is unknown until it ends, so capture progress is a byte count
// rather than a fraction.
type countingWriter struct {
	dst      io.Writer
	written  int64
	reported int64
	report   func(int64)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.written += int64(n)
	if w.written-w.reported >= progressStep {
		w.reported = w.written
		w.report(w.written)
	}
	return n, err
}
