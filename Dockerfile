FROM golang:1.25.0-alpine3.21 AS builder

WORKDIR /app

ARG APP_VERSION=dev

# Copy dependency files first (better layer caching)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY . .

# Build with cache
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
    -ldflags="-X github.com/Panonim/dynglance/internal/dynglance.buildVersion=${APP_VERSION}" .

FROM alpine:3.21

# zfs: lets gopsutil's disk-usage lookups shell out to `zfs` for accurate
# used/available stats on ZFS-backed mounts (server-stats widget).
# curl: used by the HEALTHCHECK below.
RUN apk add --no-cache zfs curl

WORKDIR /app
COPY --from=builder /app/dynglance .
RUN mkdir -p /app/config

EXPOSE 8080/tcp
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/ -o /dev/null || exit 1
ENTRYPOINT ["/app/dynglance", "--config", "/app/config/dynglance.yml"]
