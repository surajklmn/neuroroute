#!/usr/bin/env bash

# ──────────────────────────────────────────────────────────────
#  NeuroRoute — Live Service Telemetry Study Script
#
#  Starts the gateway targeting the public GitHub API and
#  records live traffic to gather telemetry in traffic.csv.
# ──────────────────────────────────────────────────────────────

set -euo pipefail

# Configuration
GATEWAY_PORT=8009
TARGET_HOST="https://api.github.com"
TRAFFIC_LOG="ml/data/traffic_study.csv"

# Trap cleanup to always stop the gateway and clean up the binary
cleanup() {
    if [ -n "${GATEWAY_PID:-}" ]; then
        echo "🛑 Stopping NeuroRoute gateway (PID: $GATEWAY_PID)..."
        kill "$GATEWAY_PID" 2>/dev/null || true
        wait "$GATEWAY_PID" 2>/dev/null || true
    fi
    rm -f scripts/gateway_bin
}
trap cleanup EXIT

echo "🔨 Compiling gateway..."
go build -o scripts/gateway_bin gateway/main.go gateway/predictor.go

echo "🧠 Starting NeuroRoute Gateway targeting live GitHub API ($TARGET_HOST)..."

# Run gateway in background
PORT="8009" \
TRAFFIC_LOG_PATH="ml/data/traffic_study.csv" \
SMART_ROUTING="false" \
FAST_WORKER_URLS="$TARGET_HOST" \
MEDIUM_WORKER_URLS="$TARGET_HOST" \
SLOW_WORKER_URLS="$TARGET_HOST" \
./scripts/gateway_bin &
GATEWAY_PID=$!

echo "⏳ Waiting for gateway to boot up on port $GATEWAY_PORT..."
for i in {1..10}; do
    if curl -s "http://localhost:$GATEWAY_PORT/ping" >/dev/null; then
        echo "✅ Gateway is active!"
        break
    fi
    if [ "$i" -eq 10 ]; then
        echo "❌ Gateway failed to start."
        exit 1
    fi
    sleep 0.5
done

echo ""
echo "📡 Dispatching study requests through the transparent proxy..."
echo "──────────────────────────────────────────────────────────────"

echo "🔍 1. Light Request: GET /users/zefrus (fetching user profile)..."
curl -s "http://localhost:$GATEWAY_PORT/users/zefrus" > /dev/null
echo "   ↳ Success!"
sleep 1

echo "🔍 2. Medium Request: GET /users/zefrus/repos (listing repos)..."
curl -s "http://localhost:$GATEWAY_PORT/users/zefrus/repos?per_page=10" > /dev/null
echo "   ↳ Success!"
sleep 1

echo "🔍 3. Heavy Request: POST /markdown (rendering Markdown body)..."
curl -s -X POST "http://localhost:$GATEWAY_PORT/markdown" \
    -H "Content-Type: application/json" \
    -d '{"text": "Hello from **NeuroRoute** Live Study!"}' > /dev/null
echo "   ↳ Success!"
sleep 1.5

echo ""
echo "📊 Study Complete!"
echo "──────────────────────────────────────────────────────────────"
if [ -f "$TRAFFIC_LOG" ]; then
    echo "📝 Captured Telemetry in $TRAFFIC_LOG:"
    echo ""
    # Print header and the last 3 rows nicely
    head -n 1 "$TRAFFIC_LOG"
    tail -n 3 "$TRAFFIC_LOG"
else
    echo "❌ Error: Telemetry file not found at $TRAFFIC_LOG."
fi
echo ""
