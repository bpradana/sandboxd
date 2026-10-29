package route

import (
	fiber "github.com/gofiber/fiber/v3"
)

func registerSandboxRoutes(params Params, api fiber.Router) {
	h := params.SandboxHandler

	api.Get("/images", h.ListImages)

	sandboxes := api.Group("/sandboxes")
	sandboxes.Post("", h.CreateSandbox)
	sandboxes.Get("", h.ListSandboxes)
	sandboxes.Get("/:id", h.GetSandbox)
	sandboxes.Delete("/:id", h.DeleteSandbox)
	sandboxes.Get("/:id/stats", h.GetSandboxStats)
	sandboxes.Post("/:id/keepalive", h.KeepaliveSandbox)

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
