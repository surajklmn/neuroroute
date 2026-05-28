# 03: Go Gateway Systems Deep Dive

This guide takes a look under the hood of the **Go Layer 7 Reverse Proxy Gateway** located in [gateway/main.go](../gateway/main.go). 

We will explore the three critical systems-level mechanisms that make our proxy highly reliable, performant, and dynamic:
1.  **Peek-and-Restore Stream Reading** (evaluating bodies without consuming them).
2.  **Weighted Least-Work L7 Routing** (lane assignment, atomic trackers, and fallback mechanics).
3.  **Automated Retrainer on Latency Drift** (circular buffers and self-correcting background loops).

---

## 1. Peek-and-Restore Stream Reading

In standard HTTP servers, the request body is an **incoming network stream** (`io.ReadCloser`). 

> [!CAUTION]
> **The Stream Drainage Problem**
> You can only read a network stream **once**. If you read the request body to inspect it for machine learning features (like counting JSON keys or looking for keywords), you "drain" the stream. 
>
> When the gateway forwards the request to a downstream worker, the worker will see an **empty body** and crash or reject the transaction.

To solve this, NeuroRoute implements a **Peek-and-Restore** pattern using `io.MultiReader`. Here is how it works step-by-step:

```text
Incoming Request Body Stream (r.Body)
         │
         ▼ (io.LimitReader to 512 bytes)
┌─────────────────────────────────┐
│ Peeked Buffer (First 512 Bytes) │  <-- Fed to Feature Extractor & ML
└────────────────┬────────────────┘
                 │
                 ▼ (Stitched back together using io.MultiReader)
┌────────────────────────────────────────────────────────┐
│ Reconstructed Body = Peeked Buffer + Remaining Stream  │  <-- Sent to Downstream Worker
└────────────────────────────────────────────────────────┘
```

### The Go Implementation
Here is a simplified code snippet of how this stream peeking is achieved inside the gateway:

```go
// 1. Limit the reader to peek at most 512 bytes
peekLimit := int64(512)
bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, peekLimit))
if err != nil {
    // Handle error gracefully
}

// 2. Perform feature extraction on the peeked body byte slice
jsonKeys := countJSONKeysSafely(bodyBytes)
keywordFreq := countHeavyKeywords(bodyBytes)

// 3. RESTORE the body stream: Create a MultiReader that joins the 
// already-read peeked bytes with the remaining unread body stream.
r.Body = io.NopCloser(io.MultiReader(
    bytes.NewReader(bodyBytes), 
    r.Body,
))
```

*   **Zero Memory Overhead:** By limiting the peek to exactly 512 bytes, we guarantee that the gateway will never load a massive file (like a 1GB upload) into memory, protecting the proxy from running out of RAM (Out of Memory crashes).

---

## 2. Weighted Least-Work L7 Routing

Once a request has been classified, it must be routed to the most appropriate worker.

Instead of a simple "Round Robin" loop (which sends requests to workers 1, 2, 3, 4 sequentially without checking if they are busy), NeuroRoute uses a **Weighted Least-Work Routing** algorithm.

### Dynamic Weight Assignments
Different traffic classes put different amounts of stress on our workers. A heavy matrix calculation puts exponentially more load on a CPU than a simple hello-world ping. Therefore, we assign atomic "weights" to requests:

*   **Class 0 (Light) Weight:** `1`
*   **Class 1 (Medium) Weight:** `10`
*   **Class 2 (Heavy) Weight:** `100`

### Atomic Active Work Tracking
The gateway keeps track of the **active, in-flight work weight** currently processing on each worker using thread-safe **Atomic Counters**:

```go
// Tracking work atomically in Go
atomic.AddInt64(&worker.ActiveWeight, 100)      // When a heavy request starts
defer atomic.AddInt64(&worker.ActiveWeight, -100) // When the request completes
```

### The Selection Loop
When a request of a specific Class arrives, the gateway:
1.  Identifies the designated **lane pool** for that Class (e.g., Workers 1 & 2 for Class 0).
2.  Filters for healthy workers.
3.  Selects the worker with the **lowest active cumulative weight**.

### Failover Resiliency
What happens if the entire Fast Lane goes down? If Worker 1 and Worker 2 both fail their health checks, we cannot just drop the request. 

NeuroRoute implements **Graceful Degradation**:

```text
Class 0 Request ➔ [ Fast Lane Pool ] (All down!)
                         │
                         ▼ (Degrade to closest Lane)
                  [ Medium Lane Pool ] (Check health & route)
                         │
                         ▼ (If Med down, degrade to Slow)
                  [ Slow Lane Pool ]
```

This cascading health-check resolution ensures that even during catastrophic hardware failures, NeuroRoute will route traffic to any surviving container rather than returning service errors to your users.

---

## 3. Automated Retrainer on Latency Drift

A major issue in production machine learning systems is **Data Drift**. Over time, the nature of client requests might change, making a trained model outdated and causing latencies to creep up.

NeuroRoute prevents this by embedding an **automated, self-correcting telemetry retrainer**.

```text
Incoming Class 0 Request ➔ Record Execution Latency
                                 │
                                 ▼ (Store in 100-size Circular Buffer)
                      ┌─────────────────────┐
                      │ [12ms, 8ms, 15ms...]│
                      └──────────┬──────────┘
                                 │
                   (Does Avg Latency > 30ms?)
                                 ├─── No  ➔ (Do Nothing)
                                 │
                                 └─── Yes ➔ [ Trigger Background Retrainer ]
                                                   │
                                                   ▼ (Asynchronous goroutine)
                                             1. make harvest (Collect new logs)
                                             2. make train   (Retrain model)
                                             3. make build   (Inline compile and deploy)
```

### How the Circular Buffer works in Go:
We maintain a thread-safe sliding slice of the last 100 successful lightweight requests:

```go
type LatencyTracker struct {
    mu        sync.Mutex
    latencies []float64
    index     int
    maxSize   int
}

func (lt *LatencyTracker) Add(latency float64) {
    lt.mu.Lock()
    defer lt.mu.Unlock()

    if len(lt.latencies) < lt.maxSize {
        lt.latencies = append(lt.latencies, latency)
    } else {
        lt.latencies[lt.index] = latency
        lt.index = (lt.index + 1) % lt.maxSize // Wrap around (Circular)
    }
}
```

If the running average exceeds **30ms**, a background goroutine is fired off to harvest the recent telemetry log CSV, invoke the Python training pipeline, auto-generate a fresh `gateway/predictor.go`, and rebuild the binary. 

This ensures that the gateway **automatically adapts to changing production traffic profiles** without requiring any developer intervention!

In the next guide, [04: Adding Custom Features](04_how_to_custom_features.md), we will walk through a step-by-step tutorial showing how you can extend the feature extractor and retrain your gateway with custom traffic signals!
