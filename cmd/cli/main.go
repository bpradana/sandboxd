// Package main implements sandboxdctl, the single shipped executable: it runs the API server and the operator commands for base images and cleanup.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	fxmodule "github.com/bpradana/sandboxd/internal/fx"
	"github.com/bpradana/sandboxd/internal/helper/runapp"
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
