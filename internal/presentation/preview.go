package presentation

import "github.com/bpradana/sandboxd/internal/preview"

type CreatePreviewRequest struct {
	Port int `json:"port" validate:"required,min=1,max=65535" example:"3000"`
}

type RevokePreviewRequest struct {
	ID string `json:"id" validate:"required,len=48,hexadecimal"`
}

type PreviewEnvelopeDoc = EnvelopeDoc[preview.Session]
