# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X main.version=docker" \
    -o /out/deepaicode2api .

FROM alpine:3.22 AS runtime

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates tzdata tini \
    && adduser -D -u 10001 app \
    && mkdir -p /data \
    && chown 10001:10001 /data

COPY --from=build /out/deepaicode2api /usr/local/bin/deepaicode2api

WORKDIR /app
USER 10001:10001

ENV DEEPAI2API_HOST=0.0.0.0 \
    DEEPAI2API_PORT=10087 \
    DEEPAI2API_DATA_PATH=/data/deepaicode2api-data.json

VOLUME ["/data"]
EXPOSE 10087

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:10087/health || exit 1

ENTRYPOINT ["/sbin/tini", "-g", "--", "deepaicode2api"]

# Optional image for Google login on servers without a desktop. Keep Chromium's
# sandbox enabled and run the browser as the same unprivileged service user.
FROM runtime AS browser
USER root
# hadolint ignore=DL3018
RUN apk add --no-cache chromium font-noto xvfb xvfb-run xauth
COPY --chmod=755 scripts/docker-browser-entrypoint.sh /usr/local/bin/docker-browser-entrypoint
USER 10001:10001
ENV DEEPAI_GOOGLE_BROWSER=/usr/bin/chromium-browser \
    DEEPAI_GOOGLE_HEADLESS=false
ENTRYPOINT ["/sbin/tini", "-g", "--", "/usr/local/bin/docker-browser-entrypoint"]

# The default build remains the smaller API-only image.
FROM runtime AS release
