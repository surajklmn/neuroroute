# ──────────────────────────────────────────────────
#  NeuroRoute Gateway — Multi-Stage Docker Build
# ──────────────────────────────────────────────────

# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /gateway main.go

# Stage 2: Run
FROM scratch

COPY --from=builder /gateway /gateway

EXPOSE 8000

ENTRYPOINT ["/gateway"]
