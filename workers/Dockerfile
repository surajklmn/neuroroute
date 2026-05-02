# ──────────────────────────────────────────────────
#  NeuroRoute Worker — Multi-Stage Docker Build
#  Final image: ~15MB static binary on scratch
# ──────────────────────────────────────────────────

# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

# Build a fully static binary (no CGO dependencies)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /worker main.go

# Stage 2: Run — minimal scratch image
FROM scratch

COPY --from=builder /worker /worker

EXPOSE 8080

ENTRYPOINT ["/worker"]
