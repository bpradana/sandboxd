package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/presentation"
)

type previewInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox running the application."`
	Port      int    `json:"port" jsonschema:"The TCP port the application listens on inside the sandbox. Find it with sandbox_ports."`
}

func addPreviewTools(server *mcp.Server, api *client.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_ports",
		Description: "List the TCP ports something is listening on inside a sandbox. Use it after starting a server to find out which port it chose, and whether a preview can reach it.",
		Annotations: &mcp.ToolAnnotations{Title: "List listening ports", ReadOnlyHint: true},
	}, handler(func(ctx context.Context, in idInput) (*mcp.CallToolResult, error) {
		ports, err := api.ListeningPorts(ctx, in.SandboxID)
		if err != nil {
			return nil, err
		}
		if len(ports) == 0 {
			return text("Nothing is listening in %s yet. Start the server in the background, for example with sandbox_exec running \"<command> &\", then check again.", in.SandboxID), nil
		}
		lines := make([]string, 0, len(ports))
		for _, port := range ports {
			note := "previewable"
			if !port.Previewable {
				note = "not previewable: bound to " + port.Address + " only, so bind it to 0.0.0.0 instead"
			}
			lines = append(lines, fmt.Sprintf("%d  (%s)", port.Port, note))
		}
		return text("Listening in %s:\n%s", in.SandboxID, strings.Join(lines, "\n")), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "sandbox_preview",
		Description: "Open a browser-accessible URL for an application running inside a sandbox, and return it. " +
			"Use this to show a running web application to a person: give them the URL. " +
			"The URL works until it expires or the sandbox stops, and anyone holding it can reach that application, so share it deliberately.",
		Annotations: &mcp.ToolAnnotations{Title: "Open an app preview"},
	}, handler(func(ctx context.Context, in previewInput) (*mcp.CallToolResult, error) {
		session, err := api.CreatePreview(ctx, in.SandboxID, in.Port)
		if err != nil {
			return nil, err
		}
		// Say when nothing is actually serving there: the URL will answer
		// "App is not reachable", and the model should start the server
		// rather than hand a broken link to a person.
		warning := ""
		if ports, portErr := api.ListeningPorts(ctx, in.SandboxID); portErr == nil {
			warning = previewWarning(ports, in.Port)
		}
		return text("Preview for port %d: %s\nIt expires at %s.%s",
			session.Port, session.URL, session.ExpiresAt.Format("15:04:05 MST"), warning), nil
	}))
}

func previewWarning(ports []presentation.ListeningPortResponse, port int) string {
	for _, listening := range ports {
		if listening.Port != port {
			continue
		}
		if !listening.Previewable {
			return fmt.Sprintf("\n\nWarning: port %d is bound to %s only, which a preview cannot reach. Rebind the server to 0.0.0.0.", port, listening.Address)
		}
		return ""
	}
	return fmt.Sprintf("\n\nWarning: nothing is listening on port %d yet, so the URL will not load until the server is started.", port)
}
