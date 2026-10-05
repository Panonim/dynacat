# Build with: docker build -f windows/Dockerfile --build-arg APP_VERSION=dev --build-arg ARCH=amd64 --output build/windows .
# ARCH can be amd64 or arm64.
FROM alpine:3.21 AS icon

RUN apk add --no-cache imagemagick
COPY internal/dynacat/static/favicon.png /icon/
RUN magick /icon/favicon.png -background none -gravity center -extent 920x920 -resize 256x256 /icon/dynacat.png \
    && magick /icon/dynacat.png -define icon:auto-resize=256,128,64,48,32,16 /icon/dynacat.ico \
    && magick /icon/dynacat.png \
    -fill '#261F3F' -stroke white -strokewidth 8 -draw 'circle 198,198 198,250' \
    -stroke none -fill white -draw 'rectangle 189,164 207,198' -draw 'polygon 172,194 224,194 198,222' \
    -draw 'roundrectangle 172,230 224,238 4,4' \
    -define icon:auto-resize=256,128,64,48,32,16 /icon/setup.ico

FROM golang:1.26-alpine AS builder

ARG APP_VERSION=dev
ARG ARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY main.go ./
COPY internal ./internal
COPY pkg ./pkg
COPY --from=icon /icon/dynacat.ico /tmp/dynacat.ico
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go run github.com/tc-hib/go-winres@v0.3.3 simply --arch ${ARCH} --icon /tmp/dynacat.ico \
    --product-name Dynacat --file-description Dynacat \
    && CGO_ENABLED=0 GOOS=windows GOARCH=${ARCH} go build \
    -ldflags "-s -w -X github.com/Panonim/dynacat/internal/dynacat.buildVersion=${APP_VERSION}" \
    -o /out/dynacat.exe .

FROM amake/innosetup:latest AS installer

ARG APP_VERSION=dev
ARG ARCH=amd64
COPY --chown=xclient windows/dynacat.iss ./
COPY --from=icon --chown=xclient /icon/setup.ico ./
COPY --from=builder --chown=xclient /out/dynacat.exe ./
RUN iscc /DAppVersion=${APP_VERSION} /DArch=${ARCH} dynacat.iss

FROM scratch

ARG ARCH=amd64
COPY --from=builder /out/dynacat.exe /
COPY --from=installer /work/output/dynacat-windows-${ARCH}-setup.exe /
