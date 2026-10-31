// Package mcp exposes sandboxd to AI agents over the Model Context Protocol.
//
// The tools are deliberately the same operations the REST API offers, with no
// privileged path of their own: an agent reaching sandboxd through MCP can do
// exactly what its API key allows and nothing more. What the tools add is
// shape -- schemas an agent can read, results summarized as text it can act
// on, and errors returned as tool failures rather than transport errors, so a
// model can correct itself instead of giving up.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
)

// Sandboxes an agent asks for without saying how long it needs them live
// until this many seconds pass, rather than the daemon's default, because an
// agent that forgets to clean up should not leave a sandbox running for long.
const defaultTTLSeconds = 1800

// defaultChangeEntries keeps a change list readable: a model asking what a
// command touched wants the shape of the answer, not every copied-up file.
const defaultChangeEntries = 200

// readLimit bounds sandbox_read_file. A model cannot usefully consume more
// than this in one tool result, and the sandbox can process bigger files
// itself with sandbox_exec.
const readLimit = 256 << 10

// NewServer builds the MCP server. Every tool runs against api, so the
// daemon's own authentication and limits apply unchanged.
func NewServer(api *client.Client, build buildinfo.Info) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "sandboxd",
		Title:   "sandboxd sandboxes",
		Version: build.Version,
	}, &mcp.ServerOptions{
		Instructions: strings.TrimSpace(`
Run untrusted code in disposable Linux sandboxes.

Create a sandbox with sandbox_create, then use sandbox_exec to run commands in
it and sandbox_write_file to put code there. Each sandbox has its own
filesystem, process table and resource limits, and is destroyed at its expiry;
call sandbox_keepalive to extend one you are still using and sandbox_destroy
as soon as you are done.

Sandboxes are created without network access. Pass network "egress" when the
work needs to install packages or reach the internet.

sandbox_exec runs a command to completion and then kills whatever it started,
so use sandbox_start_service for anything that must keep running, such as a
web server.

To show a running web application to a person: start it with
sandbox_start_service, find its port with sandbox_ports, and call
sandbox_preview for a URL they can open.`),
	})

	addSandboxTools(server, api)
	addServiceTool(server, api)
	addFileTools(server, api)
	addPreviewTools(server, api)
	return server
}

// text is a plain tool result. Results are written for a model to read: what
// happened, and the identifiers it needs for the next call.
func text(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

// fail reports a refused operation as a failed tool call rather than a
// protocol error, so the model sees why and can try something else. The
// daemon's error codes are stable and worth passing through verbatim.
func fail(err error) *mcp.CallToolResult {
	var apiErr *client.Error
	message := err.Error()
	if errors.As(err, &apiErr) {
		message = apiErr.Message
		if apiErr.Code != "" {
			message = fmt.Sprintf("%s (%s)", apiErr.Message, apiErr.Code)
		}
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

// jsonText renders a value for a model that wants the full record rather than
// the summary line.
func jsonText(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", value)
	}
	return string(encoded)
}

// handler adapts a function that returns a result and an error into the SDK's
// typed handler, turning the error into a failed tool call.
func handler[In any](run func(context.Context, In) (*mcp.CallToolResult, error)) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		result, err := run(ctx, in)
		if err != nil {
			return fail(err), nil, nil
		}
		return result, nil, nil
	}
}
