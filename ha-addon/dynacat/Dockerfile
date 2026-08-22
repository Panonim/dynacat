ARG BUILD_FROM
ARG DYNACAT_REPO=https://github.com/trooperthorn/ha_app_dynglance.git
ARG DYNACAT_REF=main

FROM --platform=$BUILDPLATFORM golang:1.25.0-alpine3.21 AS builder

ARG DYNACAT_REPO
ARG DYNACAT_REF
ARG TARGETARCH
ARG TARGETVARIANT
ARG APP_VERSION=dev

RUN apk add --no-cache git

WORKDIR /src
RUN git clone --depth 1 --branch "${DYNACAT_REF}" "${DYNACAT_REPO}" .

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/Panonim/dynacat/internal/dynacat.buildVersion=${APP_VERSION}" \
    -o /dynacat .

FROM ${BUILD_FROM}

# tzdata: honor the "timezone" add-on option; curl: used by run.sh/HEALTHCHECK.
RUN apk add --no-cache tzdata curl

WORKDIR /app
COPY --from=builder /dynacat /usr/bin/dynacat
COPY run.sh /run.sh
RUN chmod a+x /run.sh /usr/bin/dynacat

EXPOSE 8080/tcp

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/ -o /dev/null || exit 1

CMD [ "/run.sh" ]
