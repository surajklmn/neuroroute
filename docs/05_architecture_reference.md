# 05: Architectural Reference Sheet

This document serves as a search-optimized technical reference sheet for **NeuroRoute's** configurations, operating system bounds, telemetry schemas, and gateway APIs. 

Use this file to quickly look up environment variables, container limits, database schema types, and REST endpoints.

---

## 1. Environment Configurations

These variables are defined in your [docker-compose.yml](../docker-compose.yml) or local execution shell to configure gateway and worker parameters:

| Variable Name | Default Value | Purpose / Description |
| :--- | :--- | :--- |
| `PORT` | `8000` | The network port the L7 Reverse Proxy listens on. |
| `SMART_ROUTING` | `true` | Toggles the Random Forest classifier. If `false`, falls back to fast-lane default. |
| `TRAFFIC_LOG_PATH` | `/var/log/neuroroute/traffic.csv` | File path where real-time execution telemetry is recorded. |
| `FAST_WORKER_URLS` | `http://worker1:8001,http://worker2:8002` | Comma-separated list of downstream fast worker destinations. |
| `MEDIUM_WORKER_URLS` | `http://worker3:8003` | Comma-separated list of downstream medium worker destinations. |
| `SLOW_WORKER_URLS` | `http://worker4:8004,http://worker5:8005` | Comma-separated list of downstream slow worker destinations. |
| `RETRAIN_WEBHOOK_URL`| `http://ml-retrainer:8050/retrain` | Webhook URL triggered by the gateway on latency drift. |

---

## 2. Container Cgroups Bounds

These physical barriers are established inside our multi-container topology using native Docker resource limits:

| Container / Service | Image / Role | CPU limit (Shares) | Memory Bound | Lane Alignment |
| :--- | :--- | :---: | :---: | :--- |
| **`gateway`** | `gateway/Dockerfile` | **1.0 Core** | **512 MB** | Tollbooth Classifier |
| **`worker1` & `worker2`** | `workers/Dockerfile` | **0.25 Cores** | **128 MB** | Fast Lane (Class 0) |
| **`worker3`** | `workers/Dockerfile` | **0.50 Cores** | **256 MB** | Medium Lane (Class 1) |
| **`worker4` & `worker5`** | `workers/Dockerfile` | **1.00 Cores** | **512 MB** | Slow Lane (Class 2) |
| **`ml-retrainer`** | `ml/Dockerfile` | **1.0 Core** | **512 MB** | Background ML Pipeline |

---

## 3. Telemetry Log Schema (`traffic.csv`)

The gateway writes telemetry logs asynchronously into a CSV file. These columns are read by the Python retraining pipeline during model updates:

| Column Header | Data Type | Sample Value | Description |
| :--- | :--- | :--- | :--- |
| `timestamp` | `Float64` | `1780005423.8242` | Unix Epoch timestamp when the request was received. |
| `url_path` | `String` | `/work` | The target HTTP endpoint path. |
| `http_method` | `String` | `POST` | The HTTP method (GET, POST, PUT, etc.). |
| `content_length` | `Int64` | `142` | Request body length in bytes (`0` if empty). |
| `query_params` | `String` | `type=heavy` | Raw raw query string. |
| `processing_time_ms`| `Float64` | `242.8105` | Telemetry response execution duration recorded by the worker. |
| `worker_id` | `String` | `worker4` | The specific worker container that executed the task. |
| `json_key_count` | `Float64` | `4.00` | Number of JSON keys peeked inside the request body. |
| `keyword_frequency` | `Float64` | `1.00` | Occurrences of heavy keywords inside query or payload. |

---

## 4. Gateway REST API Endpoints

The L7 Reverse Proxy exposes dedicated administrative and playground endpoints on its listening port:

### `GET /ping`
*   **Purpose:** Simple gateway health check.
*   **Response Format:** `application/json`
*   **Sample Output:**
    ```json
    {
      "status": "ok",
      "service": "neuroroute-gateway",
      "smart_routing": true
    }
    ```

### `GET /status`
*   **Purpose:** Inspect health status, active weights, and recent requests routing decisions.
*   **Response Format:** `application/json`
*   **Sample Output:**
    ```json
    {
      "smart_routing": true,
      "backends": [
        { "url": "http://worker1:8001", "healthy": true, "lane": "fast", "in_flight_weight": 0 },
        { "url": "http://worker4:8004", "healthy": true, "lane": "slow", "in_flight_weight": 100 }
      ],
      "recent_requests": [
        { "timestamp": "15:43:02.124", "path": "/work", "method": "POST", "predicted": 2, "actual_route": 2, "processing_ms": 242.5, "worker_id": "worker4", "status": 200 }
      ]
    }
    ```

### `GET /dashboard`
*   **Purpose:** Serves the live HTML/CSS monitoring dashboard visualizing real-time traffic lanes distributions and active weight spikes.
*   **Response Format:** `text/html`

### `POST /dashboard/trigger?type={light|heavy}`
*   **Purpose:** Utility endpoint triggered by the dashboard UI to send mock loopback requests through the gateway routing pipeline.
*   **Response Format:** `application/json`

### `ANY /work` (and all other proxied paths)
*   **Purpose:** The main reverse-proxy path. Requests hit the embedded Random Forest, undergo Weighted Least-Work lane assignment, and get forwarded to downstream worker pools.
