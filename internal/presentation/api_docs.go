// Package presentation defines the request and response DTOs exchanged over the HTTP API.
package presentation

type ValidationErrorDoc struct {
	// Field is the request field that failed validation.
	Field string `json:"field" example:"memory_mb"`
	// Tag is the validation rule that failed.
	Tag string `json:"tag" example:"gte"`
	// Value is the rejected field value, when available.
	Value any `json:"value,omitempty" swaggertype:"string" example:"4"`
	// Param is the validation rule parameter, when the rule takes one.
	Param string `json:"param,omitempty" example:"16"`
}

type ErrorResponseDoc struct {
	// Code is the HTTP status code repeated in the response body.
	Code int `json:"code" example:"404"`
	// Message is the client-facing response message.
	Message string `json:"message" example:"sandbox not found"`
	// Entity identifies the API domain that produced the response.
	Entity string `json:"entity" example:"sandbox"`
	// Data is an empty object for error responses.
	Data map[string]any `json:"data"`
	// Error is the primary error detail when one is available.
	Error string `json:"error,omitempty" example:"sandbox not found"`
	// Errors contains field-level validation errors.
	Errors []ValidationErrorDoc `json:"errors,omitempty"`
	// ErrorCode is the machine-readable error code.
	ErrorCode string `json:"error_code,omitempty" example:"sandbox_not_found"`
	// Details carries additional structured error context when available.
	Details map[string]any `json:"details,omitempty" swaggertype:"object"`
}

type ReadinessResponseDoc struct {
	// Status is ok when the sandbox engine is ready, otherwise unhealthy.
	Status string `json:"status" example:"ok"`
	// Checks maps component names to their readiness status or error.
	Checks map[string]string `json:"checks"`
	// Build identifies the exact service binary answering the probe.
	Build BuildInfoDoc `json:"build"`
}

type BuildInfoDoc struct {
	// Version is formatted for the deployment environment.
	Version string `json:"version" example:"staging.3fa41bc"`
	// Tag is the release tag, or unknown when the build is not tagged.
	Tag string `json:"tag" example:"unknown"`
	// Commit is the seven-character Git commit hash.
	Commit string `json:"commit" example:"3fa41bc"`
	// BuildTime is the UTC image build time.
	BuildTime string `json:"build_time" example:"2026-10-29T12:00:00Z"`
	// Modified reports whether Go detected local source changes.
	Modified bool `json:"modified" example:"false"`
}

type EnvelopeDoc[T any] struct {
	// Code is the HTTP status code repeated in the response body.
	Code int `json:"code" example:"200"`
	// Message is the client-facing success message.
	Message string `json:"message" example:"sandbox request success"`
	// Entity identifies the API domain that produced the response.
	Entity string `json:"entity" example:"sandbox"`
	// Data is the response payload.
	Data T `json:"data"`
}

type EmptyEnvelopeDoc = EnvelopeDoc[map[string]any]

type SandboxEnvelopeDoc = EnvelopeDoc[SandboxResponse]

type SandboxListEnvelopeDoc = EnvelopeDoc[[]SandboxResponse]

type SandboxStatsEnvelopeDoc = EnvelopeDoc[SandboxStatsResponse]

type ImageListEnvelopeDoc = EnvelopeDoc[[]ImageResponse]

type ExecEnvelopeDoc = EnvelopeDoc[ExecResponse]

type SessionEnvelopeDoc = EnvelopeDoc[SessionResponse]

type TerminalTicketEnvelopeDoc = EnvelopeDoc[TerminalTicketResponse]

type SessionListEnvelopeDoc = EnvelopeDoc[[]SessionResponse]

type FileInfoEnvelopeDoc = EnvelopeDoc[FileInfoResponse]

type FileListEnvelopeDoc = EnvelopeDoc[[]FileInfoResponse]
