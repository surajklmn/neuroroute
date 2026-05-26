# ──────────────────────────────────────────────────
#  NeuroRoute Gateway — Multi-Stage Docker Build
# ──────────────────────────────────────────────────

# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

# Compile the entire package directory to include main.go and predictor.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /gateway .

# Stage 2: Run
FROM alpine:latest

# Install shell utilities and make so background retraining triggers can execute
RUN apk add --no-cache make git bash

COPY --from=builder /gateway /gateway

EXPOSE 8000

ENTRYPOINT ["/gateway"]
