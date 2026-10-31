package route

import (
	fiber "github.com/gofiber/fiber/v3"
)

func registerSandboxRoutes(params Params, api fiber.Router) {
	h := params.SandboxHandler

	api.Get("/capacity", h.GetCapacity)
	api.Get("/images", h.ListImages)
	api.Delete("/images/:name", h.RemoveImage)
	api.Get("/snapshots", h.ListSnapshots)
	// Before the :id route, so "progress" is not read as a snapshot ID.
	api.Get("/snapshots/progress", h.ListSnapshotJobs)
	api.Get("/snapshots/:id", h.GetSnapshot)
	api.Delete("/snapshots/:id", h.DeleteSnapshot)
	api.Post("/snapshots/:id/image", h.PromoteSnapshot)
	api.Get("/settings/cloud-storage", h.GetCloudStorage)
	api.Post("/settings/cloud-storage/test", h.TestCloudStorage)
	api.Put("/settings/cloud-storage", h.SaveCloudStorage)
	api.Delete("/settings/cloud-storage", h.DisconnectCloudStorage)

	sandboxes := api.Group("/sandboxes")
	sandboxes.Post("", h.CreateSandbox)
	sandboxes.Get("", h.ListSandboxes)
	sandboxes.Get("/:id", h.GetSandbox)
	sandboxes.Delete("/:id", h.DeleteSandbox)
	sandboxes.Get("/:id/stats", h.GetSandboxStats)
	sandboxes.Post("/:id/keepalive", h.KeepaliveSandbox)
	sandboxes.Post("/:id/snapshots", h.CreateSnapshot)
	sandboxes.Put("/:id/snapshot-policy", h.SetSnapshotPolicy)
	sandboxes.Post("/:id/previews", h.CreatePreview)
	sandboxes.Get("/:id/previews", h.ListPreviews)
	sandboxes.Delete("/:id/previews", h.RevokePreview)
	sandboxes.Get("/:id/ports", h.ListPorts)
	sandboxes.Get("/:id/changes", h.GetSandboxChanges)

	sandboxes.Post("/:id/exec", h.ExecCommand)
	sandboxes.Post("/:id/exec/stream", h.ExecCommandStream)

	sandboxes.Post("/:id/terminal/tickets", h.CreateTerminalTicket)

	sandboxes.Post("/:id/sessions", h.CreateSession)
	sandboxes.Get("/:id/sessions", h.ListSessions)
	sandboxes.Post("/:id/sessions/:session_id/exec", h.ExecSession)
	sandboxes.Delete("/:id/sessions/:session_id", h.DeleteSession)

	sandboxes.Get("/:id/files", h.DownloadFile)
	sandboxes.Put("/:id/files", h.UploadFile)
	sandboxes.Delete("/:id/files", h.DeleteFile)
	sandboxes.Get("/:id/files/list", h.ListFiles)
	sandboxes.Get("/:id/files/stat", h.StatFile)
	sandboxes.Post("/:id/files/mkdir", h.MakeDirectory)
}

func registerTerminalRoute(params Params, api fiber.Router) {
	api.Get("/sandboxes/:id/terminal", params.TerminalAuth, params.SandboxHandler.OpenTerminal)
}
