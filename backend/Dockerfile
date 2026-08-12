# syntax=docker/dockerfile:1

# ── dev: hot reload via air (docker-compose builds `target: dev`) ────────────
FROM golang:1.26 AS dev
WORKDIR /app
RUN go install github.com/air-verse/air@v1.61.7
COPY go.mod go.sum ./
RUN go mod download
CMD ["air"]

# ── build: static, stripped, version-stamped binary ──────────────────────────
# --platform=$BUILDPLATFORM keeps the toolchain native and cross-compiles via
# TARGETOS/TARGETARCH (set by buildx; empty on a plain `docker build`, in which
# case Go targets the native platform).
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /app

# Build metadata injected at link time — see internal/version/version.go for the
# exact symbol paths. Values must contain no spaces (linker tokenizes on space):
# a 40-char git SHA, a semver tag, and an RFC3339 timestamp like 2026-08-12T00:00:00Z.
ARG SHA=dev
ARG SEMVER=0.0.0-dev
ARG BUILDTIME=unknown
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download
COPY . .

# The main binary carries the version stamp; the healthcheck helper is the
# probe the distroless HEALTHCHECK calls (no shell/wget in the final image).
# Migrations are //go:embed-ed into the binary, so nothing extra is copied.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/timkrebs9/proxcloud/backend/internal/version.Commit=${SHA} \
        -X github.com/timkrebs9/proxcloud/backend/internal/version.Semver=${SEMVER} \
        -X github.com/timkrebs9/proxcloud/backend/internal/version.BuildTime=${BUILDTIME}" \
      -o /out/proxcloud ./cmd/proxcloud \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/healthcheck ./cmd/healthcheck

# ── prod: distroless, non-root, self-contained probe ─────────────────────────
# static-debian12 ships ca-certificates (needed for HTTPS to Proxmox), tzdata,
# /etc/passwd, and a nonroot user; :nonroot runs as UID 65532 by default.
FROM gcr.io/distroless/static-debian12:nonroot AS prod
ARG SHA=dev
ARG SEMVER=0.0.0-dev
ARG BUILDTIME=unknown
LABEL org.opencontainers.image.source="https://github.com/timkrebs/proxcloud" \
      org.opencontainers.image.revision="${SHA}" \
      org.opencontainers.image.version="${SEMVER}" \
      org.opencontainers.image.created="${BUILDTIME}" \
      org.opencontainers.image.title="proxcloud-backend" \
      org.opencontainers.image.description="Proxcloud API server — the sole component that talks to Proxmox."

COPY --from=build /out/proxcloud /proxcloud
COPY --from=build /out/healthcheck /healthcheck

EXPOSE 8080

# start-period covers migrations applied at boot before /api/health serves 200.
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=6 \
  CMD ["/healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/proxcloud"]
