package presentation

import "time"

type CreateSnapshotRequest struct {
	Labels map[string]string `json:"labels" validate:"omitempty,max=32,dive,keys,notblank,max=63,endkeys,max=256"`
}

type SnapshotResponse struct {
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
