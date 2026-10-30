package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/presentation"
)

type serviceInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox to start the process in."`
	Command   string `json:"command" jsonschema:"Command line that starts the process, for example python3 -m http.server 8000. Do not add a trailing ampersand; it is backgrounded for you."`
	Cwd       string `json:"cwd,omitempty" jsonschema:"Working directory to start it in."`
	LogPath   string `json:"log_path,omitempty" jsonschema:"File inside the sandbox to collect the process output, so it can be read back with sandbox_read_file. Defaults to a name derived from the command."`
}

// addServiceTool exposes the one thing one-shot exec cannot do. A session is
// a shell that outlives the call, so a job backgrounded in it keeps running;
// exec reaps its whole process group the moment the command returns, which
// quietly kills exactly the servers an agent wants to start.
func addServiceTool(server *mcp.Server, api *client.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "sandbox_start_service",
		Description: "Start a long-running process inside a sandbox, such as a web server, and return immediately while it keeps running. " +
			"Use this rather than sandbox_exec for anything that must outlive the call. " +
			"Afterwards find the port it listens on with sandbox_ports, and get a URL to share with sandbox_preview.",
		Annotations: &mcp.ToolAnnotations{Title: "Start a long-running process"},
	}, handler(func(ctx context.Context, in serviceInput) (*mcp.CallToolResult, error) {
		session, err := reuseOrOpenSession(ctx, api, in.SandboxID, in.Cwd)
		if err != nil {
			return nil, err
		}
		logPath := in.LogPath
		if logPath == "" {
			logPath = "/tmp/" + serviceName(in.Command) + ".log"
		}
		// Redirect before backgrounding, so the caller gets control back and
		// the output is still somewhere it can be read afterwards.
		launch := fmt.Sprintf("{ %s ; } >%s 2>&1 &\necho started pid $!", in.Command, shellQuote(logPath))
		result, err := api.SessionExec(ctx, in.SandboxID, session.ID, presentation.SessionExecRequest{
			Command: launch, TimeoutSeconds: 30,
		})
		if err != nil {
			return nil, err
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("could not start it: %s%s", strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout))
		}
		return text("%s\nIt keeps running after this call, in sandbox %s.\nIts output goes to %s, readable with sandbox_read_file.\nUse sandbox_ports to see what it is listening on.",
			strings.TrimSpace(result.Stdout), in.SandboxID, logPath), nil
	}))
}

// reuseOrOpenSession keeps one shell per sandbox. Opening a fresh one per
// service would leak shells and scatter each job across a different one.
func reuseOrOpenSession(ctx context.Context, api *client.Client, sandboxID, cwd string) (presentation.SessionResponse, error) {
	sessions, err := api.ListSessions(ctx, sandboxID)
	if err != nil {
		return presentation.SessionResponse{}, err
	}
	for _, session := range sessions {
		if session.State != "closed" {
			return session, nil
		}
	}
	return api.CreateSession(ctx, sandboxID, presentation.CreateSessionRequest{Cwd: cwd})
}

// serviceName derives a log filename from the command, so two services in one
// sandbox do not write over each other's output.
func serviceName(command string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, firstWord(strings.TrimSpace(command)))
	if name == "" {
		return "service"
	}
	return name
}

func firstWord(command string) string {
	if index := strings.IndexAny(command, " \t"); index >= 0 {
		return command[:index]
	}
	return command
}

// shellQuote makes a path safe to embed in the launch line, whatever the
// model passed as log_path.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
