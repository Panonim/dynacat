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
    -ldflags="-X github.com/Panonim/dynacat/internal/dynacat.buildVersion=${APP_VERSION}" .

FROM alpine:3.21

RUN apk add --no-cache zfs

WORKDIR /app
COPY --from=builder /app/dynacat .
RUN mkdir -p /app/config

EXPOSE 8080/tcp
ENTRYPOINT ["/app/dynacat", "--config", "/app/config/dynacat.yml"]
