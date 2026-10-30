package httpapi

// @title sandboxd API
// @version 1.0
// @description REST API for creating, driving, and destroying isolated sandboxes for AI agents.
// @description Sandboxes are built directly on Linux namespaces, cgroups v2, overlayfs, seccomp, and capabilities. They share the host kernel, so they are weaker than hardware-backed microVMs.
// @description The source of truth remains the codebase; this spec is generated from handler annotations.
// @BasePath /
// @schemes http https
// @tag.name health
// @tag.description Liveness and readiness probes.
// @tag.name images
// @tag.description Base root filesystems available to sandboxes.
// @tag.name sandboxes
// @tag.description Sandbox creation, inspection, lifetime extension, and destruction.
// @tag.name exec
// @tag.description One-shot command execution, buffered or streamed over Server-Sent Events.
// @tag.name sessions
// @tag.description Persistent shell sessions whose state carries across commands.
// @tag.name files
// @tag.description File upload, download, listing, and management inside a sandbox.
// @tag.name snapshots
// @tag.description Save-and-stop filesystem backups in S3-compatible storage and restore into new sandboxes.
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description API key as "Bearer <key>".
