# 🧠 NeuroRoute

### *AI-Driven Predictive Layer 7 Load Balancer & Dynamic Traffic Segregator*

[![Go Version](https://img.shields.io/badge/Go-1.22-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev)
[![Python Version](https://img.shields.io/badge/Python-3.14-3776AB?style=for-the-badge&logo=python&logoColor=white)](https://python.org)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115-009688?style=for-the-badge&logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?style=for-the-badge&logo=docker&logoColor=white)](https://docker.com)
[![k6](https://img.shields.io/badge/k6-Load--Testing-7B68EE?style=for-the-badge&logo=k6&logoColor=white)](https://k6.io)

NeuroRoute is a hybrid Go/Python Layer 7 API Gateway and load balancer designed to mitigate **Head-of-Line (HoL) blocking** in highly concurrent, mixed-traffic environments. By utilizing an embedded, low-latency Random Forest Classifier, the gateway inspects incoming request metadata in real time, classifies payloads as either *light* or *heavy*, and routes them into physically isolated worker pools (Fast Lanes vs. quarantined Slow Lanes) controlled by Docker cgroup resource limits.

---

## 🏛️ System Architecture

```
                                  INCOMING TRAFFIC
                                         │
                        ┌────────────────▼────────────────┐
                        │      Gateway (Go) :8000         │ ◄─── ML Query (<3ms)
                        │  • Extracts metadata            │       or Rule Fallback
                        │  • Decodes features             │
                        └───────┬────────────────┬────────┘
                                │                │
                       Label=0 (Light)         Label=1 (Heavy)
                                │                │
                 ┌──────────────┼──────┐         │
                 ▼              ▼      ▼         ▼
           ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
           │ Worker 1 │ │ Worker 2 │ │ Worker 3 │ │ Worker 4 │
           │  :8001   │ │  :8002   │ │  :8003   │ │  :8004   │
           │ 0.5 CPU  │ │ 0.5 CPU  │ │ 0.5 CPU  │ │ 1.0 CPU  │
           │ 256MB RAM│ │ 256MB RAM│ │ 256MB RAM│ │ 512MB RAM│
           └──────────┘ └──────────┘ └──────────┘ └──────────┘
            ◄─────── Fast Lane (Round-Robin) ──────►  ◄─ Slow Lane ─►
```

---

## 📊 Empirical Benchmarks

Here is the absolute mathematical proof of our Layer 7 segregation strategy. When subjected to a concurrent **100 Virtual User (VU) stress test** containing an 80/20 mix of light and heavy traffic, standard Round-Robin load balancing collapses, whereas **NeuroRoute completely isolates and protects light request speeds**.

### 📈 Visual Performance Breakdown
![Performance Chart](loadtests/results/comparison_chart.png)

### 📈 Comparative Performance Metrics

| Metric Pool | Baseline (Round-Robin) | NeuroRoute (ML-Segregated) | Delta / Performance Impact |
| :--- | :---: | :---: | :---: |
| **System-wide Throughput** | ~12.5 RPS | **29.0 RPS** | **+132% Throughput Boost** |
| **Light Request Average** | ~4,500 ms | **7 ms** | **99.8% Latency Reduction** |
| **Light Request p50 (Median)**| ~187 ms | **1 ms** | **99.4% Latency Reduction** |
| **Light Request p95 (Tail)** | ~42,000 ms | **38 ms** | **99.9% Latency Reduction** |
| **Light Request p99 (Max)** | ~60,000 ms | **100 ms** | **99.8% Latency Reduction** |
| **Light Request Success Rate**| ~82.0% | **100.0%** | **0% Error Rate (Complete Insulation)**|
| **Heavy Request Quarantine** | Shared Pool (Chaotic) | Isolated Pool (`worker_4`) | **0% Blast Radius Spillover** |

---

## 🛠️ The Technical Problem & Solution

### 1. The Bottleneck: Head-of-Line (HoL) Blocking
In standard microservices, short requests (like simple database pings or heartbeats) and heavy computational requests (like PDF generators, prime sieves, or matrix multiplications) hit the same downstream HTTP worker pool. 
When a surge of heavy requests hits, they fully saturate the worker threads' CPU and memory. Consequently, lightweight heartbeats get queued behind the heavy math, triggering **spurious timeouts and system-wide service failure**, even though they should take less than $1\text{ms}$.

### 2. The NeuroRoute Solution: Predictive Isolation
*   **API Gateway (Go)**: A high-performance reverse proxy written in pure Go. It intercepts requests, extracts structural metadata, and logs raw metrics asynchronously through a **non-blocking channel logger** to prevent L7 logging lag.
*   **ML Pipeline (Python)**: Engineers structural features (URI path, method, content length, query parameters) and trains a **Random Forest Classifier** with **Semi-Supervised Label Correction** to completely sanitize network queueing delays.
*   **Predictive Routing**: If the model predicts **Light (Label 0)**, the proxy routes the request round-robin to the Fast Lane (Workers 1-3). If **Heavy (Label 1)**, the proxy quarantines the request to the Slow Lane (Worker 4), guaranteeing the safety of lightweight users.

---

## 🚀 Quick Start Guide

### Prerequisites
*   [Docker & Docker Compose](https://docs.docker.com/)
*   [k6](https://k6.io/) (for executing load tests)

> 💡 **Linux Permissions Note**: If you encounter a `permission denied` error when connecting to the Docker daemon socket (e.g., on Arch Linux), run `newgrp docker` in your active terminal session to load the group privileges instantly, or prepend `sudo` to your `docker compose` commands.


### 1. Build and Run the Topology
Rebuild the Docker images and launch the containers:
```bash
make build
make up
```

### 2. Run the Smoke Test
Verify that the Go Gateway and Python ML microservice are up and healthy:
```bash
make smoke
```

### 3. Execute the Baseline Benchmark (Round-Robin)
Run the unmanaged benchmark (2 mins) to generate your noisy baseline metrics:
```bash
make test-rr
```
> 📊 **Live Monitor**: Open **`http://localhost:5665`** in your browser as soon as the test starts to view real-time latency graphs!

### 4. Train the Machine Learning Model
Harvest the metrics from the baseline run and train the predictive Random Forest model:
```bash
make harvest
make train
```

### 5. Deploy the Model and Run the Smart Benchmark
Redeploy the ML service with the new model and launch the predictive-routed benchmark:
```bash
make deploy-model
make test-smart
```
> 📊 **Live Monitor**: Watch the k6 Web Dashboard at **`http://localhost:5665`**. Notice the light request latency line remain completely flat at the bottom!

### 6. Analyze and Compare
Generate your automated comparative table and high-resolution chart:
```bash
make compare
```
The comparison chart will be exported directly to: `loadtests/results/comparison_chart.png`.

---

## 🛡️ Fail-Safes & Resiliency

NeuroRoute is built with high-availability systems engineering principles:
*   **Gateway Active Circuit-Breaker**: If a backend worker container crashes, the Gateway marks it DOWN and drops it from the active rotation pool for **30 seconds**, routing around the failure.
*   **ML Inference Fallback**: If the ML prediction microservice times out ($>50\text{ms}$) or crashes, the Gateway seamlessly falls back to standard round-robin routing to guarantee service availability.
*   **Cold-Start Classifier**: If no trained `model.pkl` is deployed in the Python service yet, it dynamically defaults to a fast rule-based heuristic classifier.

---

## 📂 Project Structure

```
├── gateway/               # Go L7 Reverse Proxy Gateway
├── workers/               # Go downstream CPU-bound workers (1-4)
├── ml/                    # Python training pipeline and FastAPI predict server
├── loadtests/             # k6 stress testing scripts and metric configurations
├── scripts/               # Statistical analytics and charting scripts
├── Makefile               # Main orchestration automation
└── docker-compose.yml     # Multi-container microservices topology
```

---

## 📜 License
MIT License.

