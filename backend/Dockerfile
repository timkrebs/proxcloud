# ── dev: hot reload via air ──────────────────────────────────────────────────
FROM golang:1.26 AS dev
WORKDIR /app
RUN go install github.com/air-verse/air@v1.61.7
COPY go.mod go.sum ./
RUN go mod download
CMD ["air"]

# ── build ────────────────────────────────────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /proxcloud ./cmd/proxcloud

# ── prod ─────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12 AS prod
COPY --from=build /proxcloud /proxcloud
EXPOSE 8080
ENTRYPOINT ["/proxcloud"]
