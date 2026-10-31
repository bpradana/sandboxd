package mcp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
)

type pathInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox holding the file."`
	Path      string `json:"path" jsonschema:"Absolute path inside the sandbox, for example /root/main.py."`
}

type writeInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox to write into."`
	Path      string `json:"path" jsonschema:"Absolute path inside the sandbox. Parent directories must already exist."`
	Content   string `json:"content" jsonschema:"The file's complete new contents."`
	Mode      string `json:"mode,omitempty" jsonschema:"Octal permissions for a newly created file, for example 0644 or 0755."`
}

type listInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"The sandbox to list files in."`
	Path      string `json:"path,omitempty" jsonschema:"Absolute directory path inside the sandbox. Defaults to /."`
}

func addFileTools(server *mcp.Server, api *client.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_write_file",
		Description: "Write a text file inside a sandbox, creating or replacing it. Use this to put source code into a sandbox before running it, rather than echoing it through a shell command.",
		Annotations: &mcp.ToolAnnotations{Title: "Write a file"},
	}, handler(func(ctx context.Context, in writeInput) (*mcp.CallToolResult, error) {
		info, err := api.WriteFile(ctx, in.SandboxID, in.Path, in.Mode, []byte(in.Content))
		if err != nil {
			return nil, err
		}
		return text("Wrote %s (%d bytes, mode %s).", info.Path, info.Size, info.Mode), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_read_file",
		Description: "Read a text file from inside a sandbox. Binary files and files larger than 256 KB are refused; process those in the sandbox with sandbox_exec instead.",
		Annotations: &mcp.ToolAnnotations{Title: "Read a file", ReadOnlyHint: true},
	}, handler(func(ctx context.Context, in pathInput) (*mcp.CallToolResult, error) {
		content, err := api.ReadFile(ctx, in.SandboxID, in.Path, readLimit)
		if err != nil {
			return nil, err
		}
		// A model cannot act on raw bytes, and sending them would waste its
		// context; say what the file is instead.
		if !utf8.Valid(content) {
			return text("%s holds %d bytes that are not valid UTF-8. Process it inside the sandbox with sandbox_exec.", in.Path, len(content)), nil
		}
		return text("%s (%d bytes):\n\n%s", in.Path, len(content), content), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sandbox_list_files",
		Description: "List a directory inside a sandbox, with each entry's type and size.",
		Annotations: &mcp.ToolAnnotations{Title: "List a directory", ReadOnlyHint: true},
	}, handler(func(ctx context.Context, in listInput) (*mcp.CallToolResult, error) {
		if in.Path == "" {
			in.Path = "/"
		}
		entries, err := api.ListFiles(ctx, in.SandboxID, in.Path)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return text("%s is empty.", in.Path), nil
		}
		lines := make([]string, 0, len(entries))
		for _, entry := range entries {
			suffix := ""
			if entry.LinkTarget != "" {
				suffix = " -> " + entry.LinkTarget
			}
			lines = append(lines, fmt.Sprintf("%-10s %9d  %s%s", entry.Type, entry.Size, entry.Name, suffix))
		}
		return text("%s:\n%s", in.Path, strings.Join(lines, "\n")), nil
	}))
}
