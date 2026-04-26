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
	docker compose logs -f worker_1 worker_2 worker_3 worker_4

logs-ml: ## Tail ML service logs
	docker compose logs -f ml_service

# ── ML Pipeline ─────────────────────────────────────────
harvest: ## Copy traffic.csv from the gateway volume to ml/data/
	mkdir -p ml/data
	docker cp neuroroute-gateway:/data/traffic.csv ml/data/traffic.csv
	@echo "✅ Harvested traffic.csv → ml/data/traffic.csv"

train: ## Train the ML model from harvested traffic data
	cd ml && python train.py
	@echo "✅ Model trained → ml/model.pkl"

deploy-model: ## Rebuild and restart the ML service with new model
	docker compose build ml_service
	docker compose up -d ml_service
	@echo "✅ ML service redeployed with new model"

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
	@echo "🔍 Pinging gateway..."
	@curl -s http://localhost:8000/ping | python3 -m json.tool || echo "❌ Gateway unreachable"
	@echo ""
	@echo "🔍 Pinging ML service..."
	@curl -s http://localhost:8050/health | python3 -m json.tool || echo "❌ ML service unreachable"

test: ## Run k6 load test (requires k6 installed)
	k6 run loadtests/traffic_profile.js

test-rr: ## Load test with round-robin only
	SMART_ROUTING=false docker compose up -d gateway
	sleep 2
	k6 run --out csv=loadtests/results/round_robin.csv loadtests/traffic_profile.js

test-smart: ## Load test with smart routing
	SMART_ROUTING=true docker compose up -d gateway
	sleep 2
	k6 run --out csv=loadtests/results/smart_route.csv loadtests/traffic_profile.js

# ── Cleanup ──────────────────────────────────────────────
clean: ## Remove all containers, volumes, and images
	docker compose down -v --rmi local
	@echo "✅ Cleaned up"

# ── Help ─────────────────────────────────────────────────
help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
