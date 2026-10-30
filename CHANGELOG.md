# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `GET /sandboxes/{id}/changes`, listing every path a sandbox's workload
  created, edited or removed, read out of the overlay's writable layer rather
  than by comparing the sandbox against its base image. Paths the daemon writes
  into every sandbox are marked seeded so they can be hidden. The file explorer
  shows it behind a Changes button, and agents get it as `sandbox_changes`.
- `POST /snapshots/{id}/image`, importing a ready snapshot as a base image, so
  the packages and sources one sandbox prepared become the starting point of
  every sandbox after it instead of being installed again on each start. The
  console offers it as "Use as image" on a snapshot card.
- S3 Transfer Acceleration for snapshot transfers, as a cloud storage setting
  and `SNAPSHOT_S3_ACCELERATE`, for daemons far from their bucket.
- `GET /snapshots/progress`, listing running backups and restores from memory
  so progress can be polled without a cloud round trip per manifest.
- Live progress for backups and restores: each snapshot card shows the phase
  it is in (capturing, uploading, verifying, downloading or importing) and how
  many bytes of the archive have moved.
- Run tabs in the web console: a command's live log on a pseudo-terminal beside
  the app it serves, in the same tab bar as the terminals, restored on reload.
- `GET /sandboxes/{id}/ports`, listing the TCP sockets listening inside a
  sandbox, read from its network namespace without running anything in it.
- A `command` query parameter on the terminal WebSocket, which runs that command
  line on the pseudo-terminal instead of an interactive shell.
- Five ways to look at a folder in the file explorer, chosen with a segmented
  control and remembered between visits: icons, list, a tree whose folders open
  in place and that reports modified time, size and kind, columns one per path
  level, and a gallery with a large preview beside the folder's other files.
- A sandbox file explorer with directory browsing, text editing, uploads,
  downloads, file and folder creation, and confirmed deletion.
- Parallel multipart snapshot uploads with 16 MiB parts, per-part retries and
  checksums, and cleanup of failed or canceled uploads.
- Parallel ranged snapshot restores and verification against S3's stored
  SHA-256 checksum to avoid a redundant download after backup when supported.
- Scheduled filesystem checkpoints that resume running sessions, automatic
  backups before TTL or idle cleanup, and retention for automatic backups.
- App previews, with per-session browser origins, HTTP/WebSocket proxying to
  sandbox loopback, expiry and revocation.
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

### Fixed

- Warnings no longer carry a stack trace. zap's development preset attached one
  to every warning, burying expected conditions such as an unauthenticated
  development API under pages of frames.

[Unreleased]: https://github.com/bpradana/sandboxd/commits/main
