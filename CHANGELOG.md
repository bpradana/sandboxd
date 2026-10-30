# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Cloud filesystem snapshots: save and stop sandboxes to S3-compatible storage,
  then restore their files and configuration into new sandboxes.
- Cloud storage settings with encrypted access keys or server identity,
  connection testing, and actionable connection errors.
- Sandboxes built directly on Linux namespaces, cgroups v2, overlayfs, seccomp, and
  capabilities, in a single static Go binary with no external container runtime.
- REST API for sandbox lifecycle, one-shot and streamed exec, persistent shell
  sessions, and file transfer, with an OpenAPI specification.
- Interactive PTY terminals over WebSocket that survive disconnects and reattach with
  scrollback replay.
- Web console at `/ui/`: create, monitor, extend, and kill sandboxes, with terminal tabs
  that persist across page reloads.
- Root inside sandboxes by default (unprivileged on the host, reduced capability set),
  so package managers work; an unprivileged `sandbox` user is also available.
- Disk-backed storage: a loop-mounted ext4 or btrfs image per sandbox, with tmpfs as an
  alternative. Unwritten data per storage device is capped relative to the sandbox's
  memory, so writing files larger than memory throttles instead of OOM-killing.
- `egress` networking with NAT and nftables rules that block private, link-local,
  metadata, and host destinations.
- Sandbox-scoped `/proc/meminfo` and a read-only `/sys/fs/cgroup`.
- TTL and idle reaping, crash reconciliation, Prometheus metrics, OpenTelemetry tracing.

[Unreleased]: https://github.com/bpradana/sandboxd/commits/main
