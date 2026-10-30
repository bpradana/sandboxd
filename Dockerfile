ARG GO_VERSION=1.26.10
ARG DEBIAN_VERSION=bookworm

# dev: toolchain image used for every Go command (tests, lint, tidy, air).
# sandboxd only runs on Linux, and local development happens on macOS, so
# nothing Go-related runs on the host.
FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS dev
WORKDIR /src

ARG GOLANGCI_LINT_VERSION=v2.13.2

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl xz-utils iproute2 nftables procps util-linux ripgrep btrfs-progs && \
    rm -rf /var/lib/apt/lists/*

RUN go install go.uber.org/mock/mockgen@latest && \
    go install mvdan.cc/gofumpt@latest && \
    go install github.com/incu6us/goimports-reviser/v3@latest && \
    go install github.com/swaggo/swag/cmd/swag@latest && \
    go install github.com/air-verse/air@latest && \
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin ${GOLANGCI_LINT_VERSION}

ENV SANDBOX_STATE_DIR=/var/lib/sandboxd

FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS build
WORKDIR /src

ARG BUILD_VERSION
ARG BUILD_DATE

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -trimpath -buildvcs=false \
    -ldflags="-s -w \
      -X github.com/bpradana/sandboxd/internal/helper/buildinfo.buildVersion=${BUILD_VERSION} \
      -X github.com/bpradana/sandboxd/internal/helper/buildinfo.buildTime=${BUILD_DATE}" \
    -o /out/sandboxdctl ./cmd/cli

# runtime: a single static executable. It must run as root with a private
# cgroup namespace and --privileged (or the equivalent capabilities) because
# it creates namespaces, cgroups, mounts, veths, and nftables rules itself.
FROM debian:${DEBIAN_VERSION}-slim AS runtime
WORKDIR /app

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata e2fsprogs btrfs-progs && \
    rm -rf /var/lib/apt/lists/*

ENV APP_ENV=production
ENV SANDBOX_STATE_DIR=/var/lib/sandboxd

EXPOSE 8080 8081
VOLUME ["/var/lib/sandboxd"]

COPY --from=build /out/sandboxdctl /app/sandboxdctl

ENTRYPOINT ["/app/sandboxdctl"]
CMD ["start"]
