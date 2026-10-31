// Package main implements sandboxdctl, the single shipped executable: it runs the API server and the operator commands for base images and cleanup.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	fxmodule "github.com/bpradana/sandboxd/internal/fx"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/helper/runapp"
	mcpserver "github.com/bpradana/sandboxd/internal/mcp"
)

// stopTimeout mirrors cmd/api: it must exceed SANDBOX_DESTROY_TIMEOUT.
const stopTimeout = 60 * time.Second

func main() {
	// The binary doubles as sandbox init and per-command shim; those
	// re-executions must not reach cobra.
	if engine.Reexec() {
		return
	}

	rootCmd := &cobra.Command{
		Use:   "sandboxdctl",
		Short: "sandboxd server and management CLI",
	}

	rootCmd.AddCommand(newStartCommand())
	rootCmd.AddCommand(newMCPCommand())
	rootCmd.AddCommand(newImageCommand())
	rootCmd.AddCommand(newCleanupCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the API server",
		Run: func(_ *cobra.Command, _ []string) {
			runapp.Run(fx.New(fxmodule.Module, fx.NopLogger, fx.StopTimeout(stopTimeout)))
		},
	}
}

// newMCPCommand serves the Model Context Protocol over stdin and stdout, the
// transport MCP clients launch a server with. It is a client of the daemon's
// REST API like any other, so it needs no privileges of its own and can point
// at a daemon on another host.
func newMCPCommand() *cobra.Command {
	var baseURL, apiKey string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve sandboxd to AI agents over the Model Context Protocol (stdio)",
		Long: "Serve sandboxd as MCP tools on stdin and stdout, so an AI agent can create sandboxes,\n" +
			"run commands and files in them, and open app previews.\n\n" +
			"The daemon must already be running. SANDBOXD_URL and SANDBOXD_API_KEY are read when\n" +
			"the matching flags are not given.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if baseURL == "" {
				baseURL = os.Getenv("SANDBOXD_URL")
			}
			if apiKey == "" {
				apiKey = os.Getenv("SANDBOXD_API_KEY")
			}
			api, err := client.New(baseURL, apiKey)
			if err != nil {
				return err
			}
			// stdout carries the protocol, so nothing else may be written there.
			return mcpserver.NewServer(api, buildinfo.Read("production")).Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&baseURL, "url", "", "sandboxd API base URL (default "+client.DefaultBaseURL+")")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key sent as a bearer token")
	return cmd
}

func newImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage base root filesystems",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "import <name> <rootfs.tar[.gz]|->",
		Short: "Import a root filesystem tarball as a base image (uid-shifted for the sandbox user namespace)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			source := os.Stdin
			if args[1] != "-" {
				file, err := os.Open(args[1])
				if err != nil {
					return fmt.Errorf("open tarball: %w", err)
				}
				defer func() { _ = file.Close() }()
				source = file
			}
			if err := engine.ImportImage(cmd.Context(), cfg.Sandbox, args[0], source); err != nil {
				return err
			}
			fmt.Printf("imported image %q\n", args[0])
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List base images",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			images, err := engine.ListImages(cfg.Sandbox)
			if err != nil {
				return err
			}
			writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(writer, "NAME\tCREATED")
			for _, image := range images {
				_, _ = fmt.Fprintf(writer, "%s\t%s\n", image.Name, image.CreatedAt.Format(time.RFC3339))
			}
			return writer.Flush()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a base image",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return engine.RemoveImage(cfg.Sandbox, args[0])
		},
	})

	return cmd
}

func newCleanupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Remove sandbox cgroups, mounts, veths, and nftables rules left by a crashed server (do not run while it is up)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			return engine.Cleanup(ctx, cfg.Sandbox, cfg.Network)
		},
	}
}
