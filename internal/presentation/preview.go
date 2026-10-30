package presentation

import "github.com/bpradana/sandboxd/internal/preview"

type CreatePreviewRequest struct {
	Port int `json:"port" validate:"required,min=1,max=65535" example:"3000"`
}

type RevokePreviewRequest struct {
	ID string `json:"id" validate:"required,len=48,hexadecimal"`
}

type PreviewEnvelopeDoc = EnvelopeDoc[preview.Session]

// ListeningPortResponse is one TCP socket accepting connections inside the
// sandbox.
type ListeningPortResponse struct {
	Port int `json:"port" example:"3000"`
	// Address is the address the socket is bound to, as the sandbox sees it.
	Address string `json:"address" example:"0.0.0.0"`
	// Previewable reports whether a preview can reach this port. Previews
	// connect over the sandbox's loopback interface, so a server bound to
	// another address is listed but cannot be previewed until it also
	// listens on 127.0.0.1 or 0.0.0.0.
	Previewable bool `json:"previewable" example:"true"`
}

type ListeningPortsEnvelopeDoc = EnvelopeDoc[[]ListeningPortResponse]

type PreviewListEnvelopeDoc = EnvelopeDoc[[]preview.Session]
