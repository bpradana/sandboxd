package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/presentation"
)

type createInput struct {
	Image          string            `json:"image,omitempty" jsonschema:"Base root filesystem, for example alpine or debian. Defaults to the daemon's configured image."`
	Network        string            `json:"network,omitempty" jsonschema:"none for loopback only (the default), or egress for outbound internet with private ranges blocked."`
	CPUMillis      int               `json:"cpu_millis,omitempty" jsonschema:"CPU quota in millicores; 1000 is one full core."`
	MemoryMB       int               `json:"memory_mb,omitempty" jsonschema:"Hard memory limit in MB. Exceeding it kills the sandbox's processes."`
	StorageMB      int               `json:"storage_mb,omitempty" jsonschema:"Writable storage limit in MB."`
	TTLSeconds     int               `json:"ttl_seconds,omitempty" jsonschema:"Lifetime in seconds, after which the sandbox is destroyed. Defaults to 1800."`
	IdleTimeoutSec int               `json:"idle_timeout_seconds,omitempty" jsonschema:"Destroy the sandbox after this many seconds with no work in it."`
	Labels         map[string]string `json:"labels,omitempty" jsonschema:"Arbitrary key/value labels to identify this sandbox later."`
}

type idInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox to act on, as returned by sandbox_create."`
}

type execInput struct {
	SandboxID      string            `json:"sandbox_id" jsonschema:"The sandbox to run the command in."`
	Command        string            `json:"command" jsonschema:"Shell command line, run through /bin/sh -c inside the sandbox."`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"Working directory; defaults to the user's home directory."`
	Env            map[string]string `json:"env,omitempty" jsonschema:"Environment variables for this command only."`
	Stdin          string            `json:"stdin,omitempty" jsonschema:"Text written to the command's standard input."`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"Kill the command after this many seconds."`
}

type changesInput struct {
	SandboxID   string `json:"sandbox_id" jsonschema:"The sandbox to inspect."`
	IncludeAll  bool   `json:"include_seeded,omitempty" jsonschema:"Also list the handful of files the daemon writes into every sandbox, such as /etc/hostname and /etc/resolv.conf. Off by default, because they are never the work you are looking for."`
	MaxEntries  int    `json:"max_entries,omitempty" jsonschema:"Stop after this many paths. Defaults to 200."`
	OnlyDeleted bool   `json:"only_deleted,omitempty" jsonschema:"List only paths the sandbox removed."`
}

type keepaliveInput struct {
	SandboxID  string `json:"sandbox_id" jsonschema:"The sandbox to keep alive."`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"New remaining lifetime in seconds, counted from now."`
}

func addSandboxTools(server *mcp.Server, api *client.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_create",
		Description: "Create a disposable Linux sandbox and return its ID. The sandbox has its own filesystem, processes and resource limits, and is destroyed when it expires. It has no network access unless network is set to egress.",
		Annotations: &mcp.ToolAnnotations{Title: "Create sandbox"},
	}, handler(func(ctx context.Context, in createInput) (*mcp.CallToolResult, error) {
		if in.TTLSeconds == 0 {
			in.TTLSeconds = defaultTTLSeconds
		}
		sandbox, err := api.CreateSandbox(ctx, presentation.CreateSandboxRequest{
			Image: in.Image, Network: in.Network,
			CPUMillis: in.CPUMillis, MemoryMB: in.MemoryMB, StorageMB: in.StorageMB,
			TTLSeconds: in.TTLSeconds, IdleTimeoutSeconds: in.IdleTimeoutSec, Labels: in.Labels,
		})
		if err != nil {
			return nil, err
		}
		return text("Created sandbox %s (%s, network %s), expires at %s.\n\n%s",
			sandbox.ID, sandbox.Image, sandbox.Network, sandbox.ExpiresAt.Format("15:04:05 MST"), jsonText(sandbox)), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_list",
		Description: "List the sandboxes this daemon currently has, with their state, limits and expiry.",
		Annotations: &mcp.ToolAnnotations{Title: "List sandboxes", ReadOnlyHint: true},
	}, handler(func(ctx context.Context, _ struct{}) (*mcp.CallToolResult, error) {
		sandboxes, err := api.ListSandboxes(ctx)
		if err != nil {
			return nil, err
		}
		if len(sandboxes) == 0 {
			return text("No sandboxes. Create one with sandbox_create."), nil
		}
		lines := make([]string, 0, len(sandboxes))
		for _, sandbox := range sandboxes {
			lines = append(lines, fmt.Sprintf("%s  %-8s  %s  network=%s  expires %s",
				sandbox.ID, sandbox.State, sandbox.Image, sandbox.Network, sandbox.ExpiresAt.Format("15:04:05")))
		}
		return text("%d sandbox(es):\n%s", len(sandboxes), strings.Join(lines, "\n")), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_exec",
		Description: "Run a shell command inside a sandbox and return its stdout, stderr and exit code. The command runs to completion and its processes are then killed, so backgrounding something here does not keep it running: use sandbox_start_service for a server or any other process that must outlive the call.",
		Annotations: &mcp.ToolAnnotations{Title: "Run a command"},
	}, handler(func(ctx context.Context, in execInput) (*mcp.CallToolResult, error) {
		result, err := api.Exec(ctx, in.SandboxID, presentation.ExecRequest{
			Command: in.Command, Cwd: in.Cwd, Env: in.Env, Stdin: in.Stdin, TimeoutSeconds: in.TimeoutSeconds,
		})
		if err != nil {
			return nil, err
		}
		var out strings.Builder
		fmt.Fprintf(&out, "exit code %d", result.ExitCode)
		if result.TimedOut {
			out.WriteString(" (timed out)")
		}
		if result.Signal != "" {
			fmt.Fprintf(&out, " (killed by %s)", result.Signal)
		}
		writeStream(&out, "stdout", result.Stdout, result.StdoutTruncated)
		writeStream(&out, "stderr", result.Stderr, result.StderrTruncated)
		return text("%s", out.String()), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_keepalive",
		Description: "Extend a sandbox's lifetime. Use this when work is still in progress and the sandbox is close to expiring.",
		Annotations: &mcp.ToolAnnotations{Title: "Extend sandbox lifetime"},
	}, handler(func(ctx context.Context, in keepaliveInput) (*mcp.CallToolResult, error) {
		sandbox, err := api.Keepalive(ctx, in.SandboxID, in.TTLSeconds)
		if err != nil {
			return nil, err
		}
		return text("Sandbox %s now expires at %s.", sandbox.ID, sandbox.ExpiresAt.Format("15:04:05 MST")), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "sandbox_changes",
		Description: "List what a sandbox's filesystem has had done to it: every file created, written to or removed since it started. " +
			"Use it to check what a command actually touched, to review your own work before handing it over, or to find where a build put its output. " +
			"It reads the sandbox's writable layer, so it costs the same whether the base image is large or small, and it never lists files the image shipped and nothing touched.",
		Annotations: &mcp.ToolAnnotations{Title: "List what changed", ReadOnlyHint: true},
	}, handler(func(ctx context.Context, in changesInput) (*mcp.CallToolResult, error) {
		changes, err := api.Changes(ctx, in.SandboxID)
		if err != nil {
			return nil, err
		}
		limit := in.MaxEntries
		if limit <= 0 {
			limit = defaultChangeEntries
		}
		lines, hidden, shown := make([]string, 0, limit), 0, 0
		for _, entry := range changes.Entries {
			if entry.Seeded && !in.IncludeAll {
				hidden++
				continue
			}
			if in.OnlyDeleted && entry.Kind != "deleted" {
				continue
			}
			if shown == limit {
				break
			}
			shown++
			lines = append(lines, fmt.Sprintf("%-9s %s%s", entry.Kind, entry.Path, changeDetail(entry)))
		}
		if len(lines) == 0 {
			return text("Nothing in %s has been changed yet.", in.SandboxID), nil
		}
		out := fmt.Sprintf("Changed in %s:\n%s", in.SandboxID, strings.Join(lines, "\n"))
		if shown == limit {
			out += "\n\nStopped at max_entries; raise it to see the rest."
		}
		if changes.Truncated {
			out += "\n\nThe daemon stopped listing before the end: this sandbox changed more paths than it will report."
		}
		if hidden > 0 {
			out += fmt.Sprintf("\n\n%d file(s) the daemon writes into every sandbox were left out; pass include_seeded to see them.", hidden)
		}
		return text("%s", out), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_destroy",
		Description: "Destroy a sandbox and everything in it. Do this as soon as you are finished, rather than leaving it to expire.",
		Annotations: &mcp.ToolAnnotations{Title: "Destroy sandbox", DestructiveHint: boolPtr(true)},
	}, handler(func(ctx context.Context, in idInput) (*mcp.CallToolResult, error) {
		if err := api.DeleteSandbox(ctx, in.SandboxID); err != nil {
			return nil, err
		}
		return text("Destroyed sandbox %s.", in.SandboxID), nil
	}))
}

// changeDetail adds what is worth knowing beyond the path: a deletion has no
// size or type to report, and a directory's size means nothing.
func changeDetail(entry presentation.SandboxChangeResponse) string {
	switch {
	case entry.Kind == "deleted":
		return ""
	case entry.Type == "dir":
		return "/"
	case entry.Type == "symlink":
		return " (symlink)"
	default:
		return fmt.Sprintf(" (%d bytes)", entry.Size)
	}
}

// writeStream appends a command's output, saying plainly when the daemon cut
// it short so a model does not treat a truncated stream as the whole answer.
func writeStream(out *strings.Builder, name, content string, truncated bool) {
	if content == "" && !truncated {
		return
	}
	fmt.Fprintf(out, "\n\n%s:\n%s", name, content)
	if truncated {
		fmt.Fprintf(out, "\n[%s was truncated by the daemon's output limit]", name)
	}
}

func boolPtr(v bool) *bool { return &v }
