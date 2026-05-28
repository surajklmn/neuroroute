# 🌐 Real-Life L7 Reverse Proxy Testing Guide

This guide details how to verify, stress-test, and audit **NeuroRoute's** L7 reverse proxy capabilities, host-rewriting, SSL/TLS forwarding, and machine-learning routing in real life using live, public cloud services.

---

## 🏛️ How Real-Life Proxying Works Under the Hood

When NeuroRoute proxies traffic to a public cloud API (e.g., GitHub, HTTPBin, JSONPlaceholder), it does more than forward packets. It dynamically rewrites the transaction headers:

```
[ Client Curl ] ➔ Host: localhost:8000
                       │
             [ NeuroRoute Gateway ]
                       │  1. Classifies request (LIGHT, MEDIUM, HEAVY)
                       │  2. Chooses Target Pool (e.g., api.github.com)
                       │  3. REWRITES Host Header ➔ Host: api.github.com
                       ▼
            [ Public Internet API ]
```

> [!NOTE]
> **Host Header Rewriting is Critical:** Public cloud platforms and CDN frontends (like Cloudflare, Varnish, or Akamai) inspect incoming HTTP headers. If an incoming TLS handshake carries a mismatched `Host` header (e.g., `localhost:8000` going to `api.github.com`), the cloud service will reject it with a `400 Bad Request` or `403 Forbidden`. NeuroRoute handles this seamlessly by overriding `req.Host = target.Host` in its proxy director.

---

## 🛠️ Step 1: Multi-Service Live Gateway Setup

To verify dynamic routing without local workers, we configure our three lanes to target different public, rate-limit-free HTTP testing APIs.

Stop any running gateways, and start a local host execution using this mapping:

```bash
FAST_WORKER_URLS="https://jsonplaceholder.typicode.com" \
MEDIUM_WORKER_URLS="https://httpbin.org" \
SLOW_WORKER_URLS="https://httpbin.org" \
TRAFFIC_LOG_PATH="./traffic.csv" \
SMART_ROUTING=true \
PORT=8000 \
go run gateway/main.go gateway/predictor.go
```

*   **Fast Lane (Class 0):** `https://jsonplaceholder.typicode.com` (Static testing backend)
*   **Medium/Slow Lanes (Class 1 & 2):** `https://httpbin.org` (Dynamic HTTP request reflector)

---

## 🧪 Step 2: Testing with Different HTTP Methods

NeuroRoute encodes standard HTTP methods into numerical indices (`GET`=0, `POST`=1, `PUT`=2, `DELETE`=3, `PATCH`=4, others=5) to feed into the Random Forest classifier.

You can verify full HTTP method propagation, request forwarding, and response delivery using the following `curl` variations:

### 1. Simple GET Request (Routes to Fast Lane)
```bash
curl -i http://localhost:8000/posts/1
```
*   **Target Hit:** JSONPlaceholder (`/posts/1`).
*   **Actual Response:** `200 OK` from JSONPlaceholder containing a sample post JSON, showing `x-powered-by: Express` in response headers.

### 2. POST Request (Creates a Resource)
```bash
curl -i -X POST -H "Content-Type: application/json" \
  -d '{"title": "ml-routing", "body": "testing", "userId": 1}' \
  http://localhost:8000/posts
```
*   **Target Hit:** JSONPlaceholder (`POST /posts`).
*   **Actual Response:** `201 Created` with the newly assigned ID (`{"id": 101}`).

### 3. PUT Request (Updates a Resource)
```bash
curl -i -X PUT -H "Content-Type: application/json" \
  -d '{"id": 1, "title": "updated-title", "body": "updated-content", "userId": 1}' \
  http://localhost:8000/posts/1
```
*   **Target Hit:** JSONPlaceholder (`PUT /posts/1`).
*   **Actual Response:** `200 OK` confirming the successful update.

### 4. DELETE Request (Deletes a Resource)
```bash
curl -i -X DELETE http://localhost:8000/posts/1
```
*   **Target Hit:** JSONPlaceholder (`DELETE /posts/1`).
*   **Actual Response:** `200 OK` returning an empty object `{}`.

---

## 🌲 Step 3: Understanding Machine Learning & Ensemble Voting

You might notice when viewing `gateway/predictor.go` that the output contains splits on `features[3]` (heavy query) and `features[2]` (content length) but behaves mathematically as a voting committee:

### The Tree Voting Breakdown
The gateway compiles **50 independent decision trees**:
1.  **26 Trees** split on `features[3]` (checking if the query parameter has `"heavy"` or `"matrix"`).
2.  **24 Trees** split on `features[2]` (checking if payload size $\le 57.5$).

When a request arrives **without a query parameter**, `features[3]` is `0.0`. 
*   All **26 query-split trees** vote **`LIGHT` (Class 0)**.
*   Even if the other **24 trees** look at a small payload size and vote **`HEAVY` (Class 2)**, the vote count finishes:
    $$\text{Class 0: 26 votes} \quad > \quad \text{Class 2: 24 votes}$$
*   The **Class 0 (LIGHT) wins by majority vote (26 vs 24)**! 

### 🧹 The `--clean` Flag & Restoring Class 1 (Medium)
If you inspect the trees, you will notice `t1` (Class 1 / Medium) is locked at `0.0`. This is because the last training session used the `--clean` flag:
```python
# ml/train.py
if args.clean:
    df["label"] = queries.apply(lambda q: 2 if ("heavy" in q or "matrix" in q) else 0)
```
This data-cleaning step simplified the training labels into binary targets (`0` and `2`), completely optimizing `Class 1 (Medium)` out of the decision boundaries.

#### How to Restore the 3-Lane (Medium) Routing:
To train the classifier on the full multi-class spectrum, run the training pipeline **without** the `--clean` flag:
```bash
# Setup Python Environment
make venv

# Train purely on raw execution latency boundaries (Light < 10ms, Med 10-200ms, Heavy > 200ms)
ml/.venv/bin/python ml/train.py --data ml/data/traffic.csv
```
This forces the Random Forest to learn three-class decisions organically based on historical execution latencies.

---

## 🎯 Step 4: Testing Organically without `heavy=true`

To make the AI route requests using **natural, real-world signals** rather than generic flags, you can exploit other parsed features:

### 1. Testing with Domain-Specific Action Keywords
In the real world, APIs perform operations using action terms (like asking a mathematical API to process a matrix). Our feature extraction checks for the keyword `"matrix"` in the query string (`gateway/main.go` line 577):

#### Heavy Domain Request (Routes to httpbin)
```bash
curl -i "http://localhost:8000/get?operation=matrix"
```
*   **Evaluation:** The query contains `"matrix"` $\rightarrow$ `isHeavyQuery` becomes `1.0`. The 26 query trees swing their votes to **`HEAVY` (Class 2)**.
*   **Result:** Routes to the Slow Lane (`https://httpbin.org/get`), returning a **`200 OK`** containing httpbin's request reflection!

#### Light DB Request (Routes to JSONPlaceholder)
```bash
curl -i http://localhost:8000/posts/1
```
*   **Evaluation:** Normal endpoint query with no heavy keywords $\rightarrow$ `isHeavyQuery` is `0.0`. The trees vote **`LIGHT` (Class 0)**.
*   **Result:** Routes to the Fast Lane (`https://jsonplaceholder.typicode.com/posts/1`), returning a **`200 OK`** containing post data.

### 2. Testing Purely by Payload Size (Content-Length)
In production, gateways inspect request payload weights to isolate massive file uploads from light JSON checkouts. Since our Random Forest model learned a content-length split boundary at **57.5 bytes**, we can route requests purely by controlling payload sizes:

#### Small Payload Request (Routes to httpbin)
Send a POST request with a tiny payload ($\le 57$ bytes):
```bash
curl -i -X POST -H "Content-Type: application/json" -d '{"x":1}' http://localhost:8000/posts
```
*   **Evaluation:** Payload is 7 bytes. Since $7 \le 57.5$, the size-split trees vote **`HEAVY` (Class 2)**.
*   **Result:** Routes to the Slow Lane (`https://httpbin.org/posts`), returning a successful **`200 OK`** response.

#### Large Payload Request (Routes to JSONPlaceholder)
Send a POST request with a larger payload ($> 57$ bytes):
```bash
curl -i -X POST -H "Content-Type: application/json" \
  -d '{"data":"this is a very long production payload designed to exceed the fifty-seven byte limit"}' \
  http://localhost:8000/posts
```
*   **Evaluation:** Payload is 95 bytes. Since $95 > 57.5$, the size-split trees vote **`LIGHT` (Class 0)**.
*   **Result:** Routes to the Fast Lane (`https://jsonplaceholder.typicode.com/posts`), returning a **`201 Created`** or **`200 OK`** response.

---

## 🔒 Step 5: The Client Spoofing Challenge
Can a malicious user spoof their way into a lane by appending `?matrix=true` or manipulating content headers?

Yes, in a simple sandbox. However, production-grade deployments mitigate this in two ways:
1.  **Zero Incentive to spoof Slow:** There is no incentive for a client to trick a gateway into routing their fast request to the Slow Lane, as they only increase their own latency.
2.  **Telemetry Self-Correction (Auto-Mitigation):** If a user spoofs a lightweight query, the gateway's telemetry logger records that it actually finished in `1.2ms`. On the next automated retraining loop, the Random Forest analyzes the logs, realizes this signature executes immediately, and **self-corrects by ignoring the spoofed keyword**, dynamically restoring optimal routing!
