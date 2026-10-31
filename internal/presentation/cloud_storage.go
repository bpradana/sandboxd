package presentation

import "time"

type CloudStorageRequest struct {
	AuthMode  string `json:"auth_mode" validate:"required,oneof=access_keys server_identity"`
	Bucket    string `json:"bucket" validate:"required,max=255"`
	Region    string `json:"region" validate:"required,max=64"`
	Endpoint  string `json:"endpoint" validate:"omitempty,max=2048"`
	Prefix    string `json:"prefix" validate:"max=1024"`
	PathStyle bool   `json:"path_style"`
	// Accelerate uses S3 Transfer Acceleration, which must also be enabled on
	// the bucket itself. AWS S3 only, and it is billed per gigabyte.
	Accelerate      bool   `json:"accelerate"`
	AccessKeyID     string `json:"access_key_id" validate:"max=256"`
	SecretAccessKey string `json:"secret_access_key" validate:"max=4096"`
	SessionToken    string `json:"session_token" validate:"max=16384"`
	// UseSavedCredentials explicitly reuses the daemon's encrypted access keys.
	UseSavedCredentials bool `json:"use_saved_credentials"`
}

// CloudStorageResponse intentionally has no credentials, including access key IDs.
type CloudStorageResponse struct {
	Connected           bool      `json:"connected"`
	Source              string    `json:"source"`
	AuthMode            string    `json:"auth_mode"`
	Bucket              string    `json:"bucket"`
	Region              string    `json:"region"`
	Endpoint            string    `json:"endpoint"`
	Prefix              string    `json:"prefix"`
	PathStyle           bool      `json:"path_style"`
	Accelerate          bool      `json:"accelerate"`
	HasSavedCredentials bool      `json:"has_saved_credentials"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type CloudStorageEnvelopeDoc = EnvelopeDoc[CloudStorageResponse]
