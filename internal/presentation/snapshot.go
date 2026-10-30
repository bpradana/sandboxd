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
}

type (
	SnapshotEnvelopeDoc     = EnvelopeDoc[SnapshotResponse]
	SnapshotListEnvelopeDoc = EnvelopeDoc[[]SnapshotResponse]
)
