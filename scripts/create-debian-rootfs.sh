#!/bin/sh
# Build a Debian rootfs on the Docker host, then import it into sandboxd.
# Usage: sh scripts/create-debian-rootfs.sh [trixie|bookworm] [image-name]
set -eu

release="${1:-trixie}"
name="${2:-debian}"
case "$release" in
  trixie|bookworm) ;;
  *) echo "Supported Debian releases: trixie, bookworm" >&2; exit 2 ;;
esac
case "$name" in
  ''|[!a-z0-9]*|*[!a-z0-9_.-]*) echo "Invalid sandbox image name" >&2; exit 2 ;;
esac
if [ "${#name}" -gt 64 ]; then echo "Image name must be at most 64 characters" >&2; exit 2; fi

cd "$(dirname "$0")/.."
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in
  aarch64|arm64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "Unsupported Docker architecture: $arch" >&2; exit 2 ;;
esac

mkdir -p target
archive="target/debian-${release}-${arch}.tar.gz"
build_image="sandboxd-rootfs-debian:${release}"
container=""
partial=""
compressed=""
cleanup() {
  if [ -n "$container" ]; then docker rm "$container" >/dev/null 2>&1 || true; fi
  if [ -n "$partial" ]; then rm -f "$partial"; fi
  if [ -n "$compressed" ]; then rm -f "$compressed"; fi
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

docker build --pull --build-arg "DEBIAN_RELEASE=$release" -t "$build_image" - <<'DOCKERFILE'
ARG DEBIAN_RELEASE=trixie
FROM debian:${DEBIAN_RELEASE}-slim
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates curl iproute2 procps python3 \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
CMD ["/bin/bash"]
DOCKERFILE

container="$(docker create "$build_image")"
partial="$(mktemp "target/.debian-rootfs.XXXXXX")"
# Export a flattened filesystem, not `docker save`'s image-layer format.
# A separate temporary tar makes a failed export visible without pipefail.
docker export --output "$partial" "$container"
compressed="$(mktemp "target/.debian-rootfs-gzip.XXXXXX")"
gzip -c "$partial" > "$compressed"
mv "$compressed" "$archive"
compressed=""
docker rm "$container" >/dev/null
container=""
rm -f "$partial"
partial=""

# Go commands run inside the dev container. Import refuses an existing image
# instead of replacing lower layers that running sandboxes may still use.
docker compose run --rm sandboxd-test go run ./cmd/cli image import "$name" "/src/$archive"
echo "Rootfs: $archive"
echo "Select image '$name' when creating a sandbox."
