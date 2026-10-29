#!/bin/sh
# Downloads a base root filesystem and imports it as a sandboxd image.
# Runs inside the dev container (mise run docker image).
#
#   scripts/fetch-image.sh alpine [version]
#   FORCE=1 scripts/fetch-image.sh alpine   # replace (stop sandboxd first)
set -eu

name="${1:-alpine}"
version="${2:-3.20.3}"

case "$(uname -m)" in
  x86_64) arch="x86_64" ;;
  aarch64 | arm64) arch="aarch64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac

case "$name" in
  alpine)
    branch="v$(echo "$version" | cut -d. -f1-2)"
    url="https://dl-cdn.alpinelinux.org/alpine/${branch}/releases/${arch}/alpine-minirootfs-${version}-${arch}.tar.gz"
    ;;
  *)
    echo "unknown image '$name' (supported: alpine)" >&2
    exit 2
    ;;
esac

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

echo "Downloading $url"
curl -fsSL "$url" -o "$workdir/rootfs.tar.gz"

if go run ./cmd/cli image list | awk 'NR > 1 { print $1 }' | grep -qx "$name"; then
  # Running sandboxes use the image as their overlay lower layer; replacing it
  # underneath them is undefined, so only do it when explicitly asked.
  if [ "${FORCE:-0}" != "1" ]; then
    echo "Image '$name' already exists; set FORCE=1 (with sandboxd stopped) to replace it."
    exit 0
  fi
  echo "Replacing existing image '$name'"
  go run ./cmd/cli image remove "$name"
fi
go run ./cmd/cli image import "$name" "$workdir/rootfs.tar.gz"
