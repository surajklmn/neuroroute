# ──────────────────────────────────────────────────────────
#  NeuroRoute — Makefile
# ──────────────────────────────────────────────────────────

.PHONY: build up down logs clean train harvest test smoke help

# ── Build ────────────────────────────────────────────────
build: ## Build all Docker images
	docker compose build

build-no-cache: ## Build all Docker images without cache
	docker compose build --no-cache

# ── Run ──────────────────────────────────────────────────
up: ## Start all services in detached mode
	docker compose up -d

up-logs: ## Start all services with live logs
	docker compose up

down: ## Stop and remove all services
	docker compose down

restart: ## Restart all services
	docker compose restart

# ── Logs ─────────────────────────────────────────────────
logs: ## Tail logs from all services
	docker compose logs -f

logs-gateway: ## Tail gateway logs only
	docker compose logs -f gateway

logs-workers: ## Tail all worker logs
	docker compose logs -f worker_1 worker_2 worker_3 worker_4 worker_5

# ── ML Pipeline ─────────────────────────────────────────
VENV := ml/.venv/bin

venv: ## Set up Python virtual environment for ML
	python -m venv ml/.venv
	$(VENV)/pip install -r ml/requirements.txt
	@echo "✅ Virtual environment ready at ml/.venv"

harvest: ## Copy traffic.csv from the gateway volume to ml/data/
	mkdir -p ml/data
	docker cp neuroroute-gateway:/data/traffic.csv ml/data/traffic.csv
	@echo "✅ Harvested traffic.csv → ml/data/traffic.csv"

train: ## Train the ML model from harvested traffic data and export inline Go predictor
	$(VENV)/python ml/train.py --data ml/data/traffic.csv --clean
	@echo "✅ Model trained and inline Go predictor generated → gateway/predictor.go"

deploy-model: ## Rebuild and restart the Go gateway service with new embedded predictor
	docker compose build gateway
	docker compose up -d gateway
	@echo "✅ Gateway redeployed with new embedded predictor"

enable-smart: ## Enable smart ML-powered routing
	docker compose down gateway
	SMART_ROUTING=true docker compose up -d gateway
	@echo "✅ Smart routing ENABLED"

disable-smart: ## Disable smart routing (fallback to round-robin)
	docker compose down gateway
	SMART_ROUTING=false docker compose up -d gateway
	@echo "✅ Smart routing DISABLED (round-robin)"

# ── Testing ──────────────────────────────────────────────
smoke: ## Quick smoke test — ping the gateway
	@echo "🔍 Pinging gateway health endpoint..."
	@curl -s http://localhost:8000/ping | python3 -m json.tool || echo "❌ Gateway unreachable"
	@echo ""
	@echo "🔍 Checking status pool information..."
	@curl -s http://localhost:8000/status | python3 -m json.tool || echo "❌ Gateway status endpoint error"

test: ## Run k6 load test (requires k6 installed)
	K6_WEB_DASHBOARD=true k6 run loadtests/traffic_profile.js

test-rr: ## Load test with round-robin only
	SMART_ROUTING=false docker compose up -d gateway
	sleep 2
	K6_WEB_DASHBOARD=true k6 run --out csv=loadtests/results/round_robin.csv loadtests/traffic_profile.js

test-smart: ## Load test with smart routing
	SMART_ROUTING=true docker compose up -d gateway
	sleep 2
	K6_WEB_DASHBOARD=true k6 run --out csv=loadtests/results/smart_route.csv loadtests/traffic_profile.js

compare: ## Compare Round-Robin vs Smart-Routing results and generate visual charts
	$(VENV)/python scripts/compare.py

# ── Cleanup ──────────────────────────────────────────────
clean: ## Remove all containers, volumes, and images
	docker compose down -v --rmi local
	@echo "✅ Cleaned up"

# ── Help ─────────────────────────────────────────────────
help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
