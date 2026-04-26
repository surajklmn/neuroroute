# 🧠 NeuroRoute

**AI-Driven Predictive Layer 7 Load Balancer** — A hybrid Go/Python system that mitigates Head-of-Line (HoL) blocking by classifying incoming HTTP requests as light/heavy via an embedded ML model and routing them to dedicated worker pools.

---

## Architecture

```
                    ┌──────────────────────────────────────┐
   Clients ────────▶│  Gateway (Go) :8000                  │
                    │  • Parses request metadata           │
                    │  • Queries ML service for prediction │
                    │  • Routes to appropriate lane        │
                    └──────┬────────────────┬──────────────┘
                           │                │
                    label=0 (light)   label=1 (heavy)
                           │                │
              ┌────────────┼───────┐        │
              ▼            ▼       ▼        ▼
         ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌──────────┐
         │Worker 1 │ │Worker 2 │ │Worker 3 │ │Worker 4  │
         │:8001    │ │:8002    │ │:8003    │ │:8004     │
         │0.5 CPU  │ │0.5 CPU  │ │0.5 CPU  │ │1.0 CPU   │
         │256MB    │ │256MB    │ │256MB    │ │512MB     │
         └─────────┘ └─────────┘ └─────────┘ └──────────┘
          ◄──── Fast Lane (RR) ────►         ◄ Slow Lane ►
                           │
                    ┌──────┴──────┐
                    │ ML Service  │
                    │ (Python)    │
                    │ :8050       │
                    └─────────────┘
```

## Tech Stack

| Component | Technology | Purpose |
|-----------|-----------|---------|
| Gateway | Go (`net/http`) | High-throughput L7 reverse proxy |
| Workers | Go (`net/http`) | CPU-bound task execution |
| ML Training | Python (scikit-learn) | Random Forest classifier |
| ML Serving | Python (FastAPI) | Sub-5ms prediction API |
| Load Testing | k6 (JavaScript) | Traffic generation & benchmarking |
| Orchestration | Docker Compose | Service topology & resource limits |

## Quick Start

```bash
# 1. Build all services
make build

# 2. Start the system
make up

# 3. Smoke test
make smoke

# 4. Generate training data (run load test with round-robin)
make test-rr

# 5. Harvest traffic logs
make harvest

# 6. Train the ML model
make train

# 7. Deploy the model and enable smart routing
make deploy-model
make enable-smart

# 8. Benchmark with smart routing
make test-smart
```

## Worker Endpoints

| Endpoint | Type | What It Does |
|----------|------|-------------|
| `GET /ping` | Light | Instant health check |
| `POST /work?type=light` | Light | SHA-256 hash × 1000 |
| `POST /work?type=heavy` | Heavy | Sieve of Eratosthenes (N=50000) |
| `POST /work?type=matrix` | Heavy | 500×500 matrix multiplication |

## ML Pipeline

1. **Data Collection**: Gateway logs every request to `traffic.csv`
2. **Labeling**: `processing_time_ms < 200` → light (0), else heavy (1)
3. **Features**: `url_path_encoded`, `method_encoded`, `content_length`, `is_heavy_query`
4. **Model**: `RandomForestClassifier(n_estimators=50, max_depth=8)`
5. **Serving**: FastAPI `POST /predict` with < 5ms latency budget

## Fail-Safes

- **ML timeout/failure** → Falls back to Fast Lane (round-robin)
- **Worker down** → Dropped from rotation for 30 seconds
- **No model loaded** → Rule-based fallback classification

## Commands

Run `make help` to see all available commands.

## License

MIT
