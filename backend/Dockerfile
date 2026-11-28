# ── Build stage ───────────────────────────────────────────────────────────────
# The compiler runs on the build host and cross-compiles for the target, so a
# multi-platform build never emulates the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app

# Modules live in a BuildKit cache mount, not in the layer, so the build step
# mounts the same cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO stays off: nothing here needs a C library, so the result is a static
# binary that runs on any base image. The build cache persists across builds.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /lofi-radio ./cmd/api

# ── Runtime stage ─────────────────────────────────────────────────────────────
# Alpine rather than scratch. It costs about 8 MB, and buys a shell for
# debugging, wget for the healthcheck below, and the ability to create /data
# with the right ownership so the Bloom filter can be written by a non-root user.
FROM alpine:3.24

# wget comes from busybox in the base image, so only certificates and zone data
# are actually needed here.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 -h /home/lofi lofi \
    && mkdir -p /data /music \
    && chown -R lofi:lofi /data

COPY --from=builder /lofi-radio /usr/local/bin/lofi-radio
COPY --from=builder /app/config.example.toml /config.toml

USER lofi
WORKDIR /

EXPOSE 8080

ENV PORT=8080 \
    BLOOM_PATH=/data/lofi-radio.bloom \
    MUSIC_DIR=/music \
    BASE_URL=http://localhost:8080

# The station is up when it answers /health. Compose uses this to hold the
# frontend back until the backend is actually accepting requests.
HEALTHCHECK --interval=15s --timeout=3s --start-period=300s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

CMD ["lofi-radio"]
