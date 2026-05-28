# 04: Adding Custom Features & Retraining

This step-by-step developer tutorial guides you through extending **NeuroRoute's** machine learning routing classifier. 

We will add a brand new feature—a custom HTTP Header `X-Priority: high`—into our classification feature vector, update feature extraction in Go, modify the Python ML pipeline, and run our inline compilation builder to deploy the new routing logic!

---

## Our Goal
We want NeuroRoute to detect if an incoming request carries an `X-Priority: high` header, and factor this signal into its routing decisions.

*   If `X-Priority: high` is present $\rightarrow$ Feature value is `1.0`.
*   Otherwise $\rightarrow$ Feature value is `0.0`.

---

## Step 1: Update the Go Gateway Feature Extractor

First, we need to extract this header from the incoming request and inject it into our feature vector.

### 1. Open `gateway/main.go`
Locate the request handler inside `ServeHTTP` where features are engineered (around line 567).

### 2. Extract the Header Feature
Add a block of code to parse the custom header:

```go
// ── Custom Feature Engineering: Priority Header ──
priorityHeader := 0.0
if strings.ToLower(r.Header.Get("X-Priority")) == "high" {
    priorityHeader = 1.0
}
```

### 3. Append to the Feature Slice
Locate the `features := []float64{...}` slice around line 584 and add the new feature at the bottom:

```go
features := []float64{
    urlPathEncoded,
    methodEncoded,
    contentLength,
    isHeavyQuery,
    jsonKeyCount,
    keywordFrequency,
    priorityHeader, // <-- Our new 7th feature!
}
```

> [!WARNING]
> **Order is Critical!**
> The order of features in the Go slice **must exactly match** the column order in your Python training pipeline. If they are mismatched, the model will inspect the wrong values (e.g. evaluating payload size as a priority value), causing erratic routing decisions!

---

## Step 2: Log the New Feature for Telemetry

Before the model can learn what `X-Priority` means, we must record this value in our historical telemetry logs (`traffic.csv`).

### 1. Update the CSV Logger Header
In `gateway/main.go`, locate `NewTrafficLogger` (around line 189) and add `"priority_header"` to the CSV columns list:

```go
w.Write([]string{
    "timestamp", "url_path", "http_method",
    "content_length", "query_params",
    "processing_time_ms", "worker_id",
    "json_key_count", "keyword_frequency",
    "priority_header", // <-- Add header name
})
```

### 2. Write the Value in the Flusher
Scroll down to the `flusher()` loop (around line 225) and append the parsed value:

```go
tl.writer.Write([]string{
    fmt.Sprintf("%.6f", entry.Timestamp),
    entry.URLPath,
    entry.HTTPMethod,
    strconv.FormatInt(entry.ContentLength, 10),
    entry.QueryParams,
    fmt.Sprintf("%.4f", entry.ProcessingMs),
    entry.WorkerID,
    fmt.Sprintf("%.2f", entry.JSONKeyCount),
    fmt.Sprintf("%.2f", entry.KeywordFrequency),
    fmt.Sprintf("%.2f", entry.PriorityHeader), // <-- Append new value
})
```

*(Note: Remember to add `PriorityHeader float64` to the `TrafficLog` struct definition at the top of the file!)*

---

## Step 3: Update the Python ML Pipeline

Now, we need to instruct our Python training script (`ml/train.py`) to read this new column and feed it to the Random Forest model.

### 1. Open `ml/train.py`
Locate where feature engineering and column selection occur.

### 2. Append the Feature to the Training Set
Add the column to your pandas features list:

```python
# ml/train.py - Locate feature columns array
feature_cols = [
    "url_path_encoded",
    "method_encoded",
    "content_length",
    "is_heavy_query",
    "json_key_count",
    "keyword_frequency",
    "priority_header"  # <-- Tell Python to train on this feature!
]

X = df[feature_cols]
y = df["label"]
```

---

## Step 4: Harvest, Train, & Compile

With both components updated, we can run our automated compiler loop!

### 1. Generate New Telemetry Logs
Run a smoke test or load test using the new code to populate `traffic.csv` with the new column entries:
```bash
make smoke
```

### 2. Run the Harvesting Script
Harvest the telemetry CSV from the gateway containers:
```bash
make harvest
```

### 3. Train the Classifier
Run the Python training script. This trains the Random Forest model on 7 features instead of 6, and **automatically generates a fresh `gateway/predictor.go` containing 7-feature evaluations**:
```bash
make train
```

### 4. Deploy and Run Smart Routing
Rebuild the gateway with the newly compiled predictor and deploy:
```bash
make build
make up
```

---

## Step 5: Verify the Model Behavior

Let's test if the gateway successfully routes traffic based on your custom header!

Execute a `curl` request containing the high priority header:
```bash
curl -i -H "X-Priority: high" http://localhost:8000/posts
```

### What to check in Gateway Logs:
Watch the docker logs using `docker compose logs -f gateway`. You should see the gateway printing incoming feature vectors:

```text
[PREDICT] Features evaluated: [120.00, 1.00, 42.00, 0.00, 0.00, 0.00, 1.00] -> Classified: Class 0 (Fast Lane)
                                                                      ▲
                                                     Our custom X-Priority: high signal!
```

Congratulations! You have successfully added a custom machine learning feature, retrained the classifier, inline-compiled the Python estimators into Go assembly, and deployed it to production!
