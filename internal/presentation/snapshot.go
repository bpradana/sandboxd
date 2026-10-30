package presentation

import "time"

type CreateSnapshotRequest struct {
	// KeepRunning freezes writers during capture, then resumes before upload.
	KeepRunning bool              `json:"keep_running"`
	Trigger     string            `json:"-"`
	Labels      map[string]string `json:"labels" validate:"omitempty,max=32,dive,keys,notblank,max=63,endkeys,max=256"`
}

type SnapshotResponse struct {
	Mode          string            `json:"mode,omitempty"`
	Trigger       string            `json:"trigger,omitempty"`
	ID            string            `json:"id"`
	SandboxID     string            `json:"sandbox_id"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	SizeBytes     int64             `json:"size_bytes"`
	SHA256        string            `json:"sha256,omitempty"`
	Architecture  string            `json:"architecture"`
	Labels        map[string]string `json:"labels,omitempty"`
	FailureReason string            `json:"failure_reason,omitempty"`
	// Progress is present only while a backup or restore of this snapshot is
	// running on this host. It is live state, not part of the stored manifest.
	Progress *SnapshotProgressResponse `json:"progress,omitempty"`
}

// SnapshotProgressResponse is how far a running backup or restore has got.
type SnapshotProgressResponse struct {
	// Operation is backup or restore.
	Operation string `json:"operation" example:"backup"`
	// Phase is capturing, uploading or verifying for a backup, and
	// downloading or importing for a restore.
	Phase string `json:"phase" example:"uploading"`
	// BytesDone counts archive bytes finished in this phase.
	BytesDone int64 `json:"bytes_done" example:"50331648"`
	// BytesTotal is the archive size, or 0 while capture is still deciding it.
	BytesTotal int64 `json:"bytes_total,omitempty" example:"202375168"`
}

// SnapshotJobResponse is one running backup or restore. Listing jobs reads
// only this host's memory, so a client can watch progress closely without
// paying for a cloud round trip per poll the way listing snapshots does.
type SnapshotJobResponse struct {
	SnapshotID string                   `json:"snapshot_id" example:"snap-4f1c2a9b8e7d6c5b4a3f2e1d"`
	Progress   SnapshotProgressResponse `json:"progress"`
}

type (
	SnapshotEnvelopeDoc     = EnvelopeDoc[SnapshotResponse]
	SnapshotListEnvelopeDoc = EnvelopeDoc[[]SnapshotResponse]
	SnapshotJobsEnvelopeDoc = EnvelopeDoc[[]SnapshotJobResponse]
)
