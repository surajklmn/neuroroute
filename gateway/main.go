// ──────────────────────────────────────────────────────────────
//  NeuroRoute — AI-Driven Reverse Proxy Gateway (Embedded ML)
//
//  A production-grade Layer 7 reverse proxy that:
//    1. Intercepts incoming HTTP requests
//    2. Profiles request body structurally (<512 bytes)
//    3. Extracts path, method, body, and query features
//    4. Predicts weight class (0, 1, 2) in-line (<10µs)
//    5. Routes using Weighted Least-Work load balancing across 3 lanes
//    6. Tracks sliding window latency for drift detection
// ──────────────────────────────────────────────────────────────

package main

import (
	"bytes"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Configuration ───────────────────────────────────────────

type Config struct {
	FastWorkerURLs   []string
	MediumWorkerURLs []string
	SlowWorkerURLs   []string
	SmartRouting     bool
	TrafficLogPath   string
	Port             string
}

func loadConfig() Config {
	cfg := Config{
		TrafficLogPath: getEnv("TRAFFIC_LOG_PATH", "/data/traffic.csv"),
		Port:           getEnv("PORT", "8000"),
	}

	fastStr := getEnv("FAST_WORKER_URLS", "http://worker_1:8080,http://worker_2:8080")
	cfg.FastWorkerURLs = strings.Split(fastStr, ",")

	mediumStr := getEnv("MEDIUM_WORKER_URLS", "http://worker_3:8080")
	cfg.MediumWorkerURLs = strings.Split(mediumStr, ",")

	slowStr := getEnv("SLOW_WORKER_URLS", "http://worker_4:8080,http://worker_5:8080")
	cfg.SlowWorkerURLs = strings.Split(slowStr, ",")

	cfg.SmartRouting = getEnv("SMART_ROUTING", "false") == "true"

	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Backend Worker ──────────────────────────────────────────

type Backend struct {
	URL            *url.URL
	Proxy          *httputil.ReverseProxy
	Healthy        bool
	DownSince      time.Time
	InFlightWeight atomic.Int64 // Track cumulative weight of in-flight requests
	mu             sync.RWMutex
}

func NewBackend(rawURL string) (*Backend, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid worker URL %q: %w", rawURL, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)

	// Rewrite Host header for reverse proxying to support live/external web services
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = u.Host
	}

	// Customize the proxy's error handler to not panic
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("⚠️  Proxy error for %s: %v", u.Host, err)
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{
			"error":  "backend unavailable",
			"target": u.Host,
		})
	}

	return &Backend{
		URL:     u,
		Proxy:   proxy,
		Healthy: true,
	}, nil
}

func (b *Backend) IsHealthy() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.Healthy {
		// Check if 30 seconds have passed since going down
		if time.Since(b.DownSince) > 30*time.Second {
			// Attempt recovery
			return true
		}
		return false
	}
	return true
}

func (b *Backend) MarkDown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Healthy = false
	b.DownSince = time.Now()
	log.Printf("🔴 Worker %s marked DOWN — will retry in 30s", b.URL.Host)
}

func (b *Backend) MarkUp() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.Healthy {
		log.Printf("🟢 Worker %s recovered", b.URL.Host)
	}
	b.Healthy = true
}

// ── Traffic Logger (Async CSV) ──────────────────────────────

type TrafficLog struct {
	Timestamp        float64
	URLPath          string
	HTTPMethod       string
	ContentLength    int64
	QueryParams      string
	ProcessingMs     float64
	WorkerID         string
	JSONKeyCount     float64
	KeywordFrequency float64
}

type TrafficLogger struct {
	ch     chan TrafficLog
	writer *csv.Writer
	file   *os.File
}

func NewTrafficLogger(path string) (*TrafficLogger, error) {
	// Ensure parent directory exists
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		if dir := path[:idx]; dir != "" {
			os.MkdirAll(dir, 0755)
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open traffic log: %w", err)
	}

	w := csv.NewWriter(f)

	// Write header if file is new (empty)
	info, _ := f.Stat()
	if info.Size() == 0 {
		w.Write([]string{
			"timestamp", "url_path", "http_method",
			"content_length", "query_params",
			"processing_time_ms", "worker_id",
			"json_key_count", "keyword_frequency",
		})
		w.Flush()
	}

	tl := &TrafficLogger{
		ch:     make(chan TrafficLog, 10000), // buffered channel
		writer: w,
		file:   f,
	}

	// Start async flush goroutine
	go tl.flusher()

	return tl, nil
}

func (tl *TrafficLogger) Log(entry TrafficLog) {
	select {
	case tl.ch <- entry:
	default:
		log.Println("⚠️  Traffic log channel full, dropping entry")
	}
}

func (tl *TrafficLogger) flusher() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case entry := <-tl.ch:
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
			})
		case <-ticker.C:
			tl.writer.Flush()
		}
	}
}

func (tl *TrafficLogger) Close() {
	tl.writer.Flush()
	tl.file.Close()
}

// ── Feature Encoders ─────────────────────────────────────────

func encodePath(path string) float64 {
	h := md5.Sum([]byte(path))
	hexStr := hex.EncodeToString(h[:])[:8]
	val, _ := strconv.ParseInt(hexStr, 16, 64)
	return float64(val % 1000)
}

func encodeMethod(method string) float64 {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case "GET":
		return 0
	case "POST":
		return 1
	case "PUT":
		return 2
	case "DELETE":
		return 3
	case "PATCH":
		return 4
	default:
		return 5
	}
}

func countJSONKeys(body []byte) float64 {
	start := 0
	for start < len(body) && (body[start] == ' ' || body[start] == '\t' || body[start] == '\n' || body[start] == '\r') {
		start++
	}
	if start >= len(body) || (body[start] != '{' && body[start] != '[') {
		return 0
	}

	count := 0
	inString := false
	escaped := false
	for i := start; i < len(body); i++ {
		ch := body[i]
		if ch == '\\' && inString {
			escaped = !escaped
			continue
		}
		if ch == '"' && !escaped {
			inString = !inString
		}
		if ch == ':' && !inString {
			count++
		}
		escaped = false
	}
	return float64(count)
}

func countHeavyKeywords(body []byte) float64 {
	bodyStr := strings.ToLower(string(body))
	keywords := []string{"heavy", "matrix", "sieve", "join", "select"}
	count := 0
	for _, kw := range keywords {
		count += strings.Count(bodyStr, kw)
	}
	return float64(count)
}

// ── Drift Monitor ────────────────────────────────────────────

type DriftMonitor struct {
	mu         sync.Mutex
	history    []float64
	index      int
	isTraining bool
}

func NewDriftMonitor() *DriftMonitor {
	return &DriftMonitor{
		history: make([]float64, 0, 100),
	}
}

func (dm *DriftMonitor) Add(val float64) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if len(dm.history) < 100 {
		dm.history = append(dm.history, val)
	} else {
		dm.history[dm.index] = val
		dm.index = (dm.index + 1) % 100
	}

	if len(dm.history) == 100 {
		var sum float64
		for _, v := range dm.history {
			sum += v
		}
		avg := sum / 100.0
		if avg > 30.0 && !dm.isTraining {
			log.Printf("[DRIFT_DETECTED] Average Class 0 execution time is %.2fms (> 30ms). Triggering background retraining...", avg)
			dm.isTraining = true
			go dm.triggerRetraining()
		}
	}
}

func (dm *DriftMonitor) triggerRetraining() {
	defer func() {
		dm.mu.Lock()
		dm.isTraining = false
		dm.mu.Unlock()
	}()

	webhookURL := getEnv("RETRAIN_WEBHOOK_URL", "http://host.docker.internal:8050/retrain")
	log.Printf("🔄 Retraining triggered by Drift Detector: sending POST request to webhook %s...", webhookURL)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewBuffer([]byte(`{"trigger": "drift_detected", "source": "gateway"}`)))
	if err != nil {
		log.Printf("[DRIFT_WARN] Webhook retraining call failed: %v. Offline retraining recommended.", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		log.Printf("[DRIFT_WARN] Webhook returned status %s. Offline retraining recommended.", resp.Status)
		return
	}

	log.Println("✅ Webhook retraining request successfully processed!")
}

// ── Router ──────────────────────────────────────────────────

type RecentRequest struct {
	Timestamp    string  `json:"timestamp"`
	Path         string  `json:"path"`
	Method       string  `json:"method"`
	PayloadSize  int64   `json:"payload_size"`
	Predicted    int     `json:"predicted"`
	ActualRoute  int     `json:"actual_route"`
	ProcessingMs float64 `json:"processing_ms"`
	WorkerID     string  `json:"worker_id"`
	Status       int     `json:"status"`
}

type Router struct {
	fastLane      []*Backend // Class 0
	mediumLane    []*Backend // Class 1
	slowLane      []*Backend // Class 2
	logger        *TrafficLogger
	driftMonitor  *DriftMonitor
	smartMode     bool
	historyMu     sync.RWMutex
	recentHistory []RecentRequest
}

func (rt *Router) AddRequestToHistory(req RecentRequest) {
	rt.historyMu.Lock()
	defer rt.historyMu.Unlock()
	rt.recentHistory = append([]RecentRequest{req}, rt.recentHistory...)
	if len(rt.recentHistory) > 20 {
		rt.recentHistory = rt.recentHistory[:20]
	}
}

func NewRouter(cfg Config) (*Router, error) {
	var fastLane []*Backend
	for _, u := range cfg.FastWorkerURLs {
		b, err := NewBackend(u)
		if err != nil {
			return nil, err
		}
		fastLane = append(fastLane, b)
	}

	var mediumLane []*Backend
	for _, u := range cfg.MediumWorkerURLs {
		b, err := NewBackend(u)
		if err != nil {
			return nil, err
		}
		mediumLane = append(mediumLane, b)
	}

	var slowLane []*Backend
	for _, u := range cfg.SlowWorkerURLs {
		b, err := NewBackend(u)
		if err != nil {
			return nil, err
		}
		slowLane = append(slowLane, b)
	}

	logger, err := NewTrafficLogger(cfg.TrafficLogPath)
	if err != nil {
		return nil, err
	}

	return &Router{
		fastLane:      fastLane,
		mediumLane:    mediumLane,
		slowLane:      slowLane,
		logger:        logger,
		driftMonitor:  NewDriftMonitor(),
		smartMode:     cfg.SmartRouting,
		recentHistory: make([]RecentRequest, 0, 20),
	}, nil
}

// selectWeightedBackend selects the healthy backend in a pool with the lowest cumulative in-flight weight.
func (rt *Router) selectWeightedBackend(pool []*Backend) *Backend {
	var best *Backend
	var minWeight int64 = math.MaxInt64

	for _, b := range pool {
		if b.IsHealthy() {
			w := b.InFlightWeight.Load()
			if w < minWeight {
				minWeight = w
				best = b
			}
		}
	}
	return best
}

// getBackendForClass implements Weighted Least-Work selection with a robust cascading fallback structure.
func (rt *Router) getBackendForClass(class int) (*Backend, int) {
	var target *Backend
	actualClass := class

	switch class {
	case 0:
		// Target: Fast Lane
		target = rt.selectWeightedBackend(rt.fastLane)
		if target == nil {
			log.Println("⚠️ All Fast Lane workers down, falling back to Medium Lane")
			target = rt.selectWeightedBackend(rt.mediumLane)
			actualClass = 1
		}
		if target == nil {
			log.Println("⚠️ All Fast/Medium Lane workers down, falling back to Slow Lane")
			target = rt.selectWeightedBackend(rt.slowLane)
			actualClass = 2
		}
	case 1:
		// Target: Medium Lane
		target = rt.selectWeightedBackend(rt.mediumLane)
		if target == nil {
			log.Println("⚠️ Medium Lane worker down, falling back to Fast Lane")
			target = rt.selectWeightedBackend(rt.fastLane)
			actualClass = 0
		}
		if target == nil {
			log.Println("⚠️ All Medium/Fast Lane workers down, falling back to Slow Lane")
			target = rt.selectWeightedBackend(rt.slowLane)
			actualClass = 2
		}
	case 2:
		// Target: Slow Lane
		target = rt.selectWeightedBackend(rt.slowLane)
		if target == nil {
			log.Println("⚠️ All Slow Lane workers down, falling back to Medium Lane")
			target = rt.selectWeightedBackend(rt.mediumLane)
			actualClass = 1
		}
		if target == nil {
			log.Println("⚠️ All Slow/Medium Lane workers down, falling back to Fast Lane")
			target = rt.selectWeightedBackend(rt.fastLane)
			actualClass = 0
		}
	}

	return target, actualClass
}

// ServeHTTP is the main L7 request handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// ── Direct endpoints on the gateway itself ──
	if r.URL.Path == "/ping" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "ok",
			"service":       "neuroroute-gateway",
			"smart_routing": rt.smartMode,
		})
		return
	}

	if r.URL.Path == "/status" {
		rt.handleStatus(w, r)
		return
	}

	if r.URL.Path == "/dashboard" {
		rt.handleDashboard(w, r)
		return
	}

	if r.URL.Path == "/dashboard/trigger" {
		rt.handleDashboardTrigger(w, r)
		return
	}

	// ── Structural Body Profiling ──
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, 512))
		if err == nil {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), r.Body))
		}
	}

	jsonKeyCount := countJSONKeys(bodyBytes)
	keywordFrequency := countHeavyKeywords(bodyBytes)

	// ── Feature Engineering ──
	urlPathEncoded := encodePath(r.URL.Path)
	methodEncoded := encodeMethod(r.Method)
	contentLength := float64(r.ContentLength)
	if contentLength < 0 {
		contentLength = 0
	}

	queryParams := r.URL.RawQuery
	isHeavyQuery := 0.0
	if strings.Contains(strings.ToLower(queryParams), "heavy") || strings.Contains(strings.ToLower(queryParams), "matrix") {
		isHeavyQuery = 1.0
	}

	// ── Embedded Prediction ──
	var predictedClass int
	if rt.smartMode {
		features := []float64{
			urlPathEncoded,
			methodEncoded,
			contentLength,
			isHeavyQuery,
			jsonKeyCount,
			keywordFrequency,
		}
		predictedClass = Predict(features)
	} else {
		// Dumb mode: fallback to class 0
		predictedClass = 0
	}

	// ── Route selection & failover ──
	target, actualClass := rt.getBackendForClass(predictedClass)
	if target == nil {
		http.Error(w, `{"error": "no healthy backends available"}`, http.StatusServiceUnavailable)
		return
	}

	// Define weight mapping: Class 0 = 1, Class 1 = 10, Class 2 = 100
	var classWeight int64
	switch actualClass {
	case 0:
		classWeight = 1
	case 1:
		classWeight = 10
	case 2:
		classWeight = 100
	default:
		classWeight = 1
	}

	// Track in-flight weight atomically
	target.InFlightWeight.Add(classWeight)
	defer func() {
		newWeight := target.InFlightWeight.Add(-classWeight)
		if newWeight < 0 {
			target.InFlightWeight.Store(0)
		}
	}()

	// ── Custom response writer to capture worker ID and status ──
	crw := &captureResponseWriter{ResponseWriter: w}

	// ── Proxy the request ──
	target.Proxy.ServeHTTP(crw, r)

	if crw.statusCode >= 502 {
		log.Printf("⚠️  Proxy returned status %d for backend %s", crw.statusCode, target.URL.Host)
	}

	// ── Capture Telemetry ──
	execTimeMsStr := crw.Header().Get("X-Execution-Time-Ms")
	var processingMs float64
	if execTimeMsStr != "" {
		if val, err := strconv.ParseFloat(execTimeMsStr, 64); err == nil {
			processingMs = val
		}
	}
	if processingMs == 0 {
		processingMs = float64(time.Since(start).Nanoseconds()) / 1e6
	}

	workerID := crw.Header().Get("X-Worker-ID")
	if workerID == "" {
		workerID = target.URL.Host
	}

	// Log traffic asynchronously
	rt.logger.Log(TrafficLog{
		Timestamp:        float64(start.UnixNano()) / 1e9,
		URLPath:          r.URL.Path,
		HTTPMethod:       r.Method,
		ContentLength:    r.ContentLength,
		QueryParams:      queryParams,
		ProcessingMs:     processingMs,
		WorkerID:         workerID,
		JSONKeyCount:     jsonKeyCount,
		KeywordFrequency: keywordFrequency,
	})

	// Add to in-memory history log for dashboard visualization
	rt.AddRequestToHistory(RecentRequest{
		Timestamp:    time.Now().Format("15:04:05.000"),
		Path:         r.URL.Path,
		Method:       r.Method,
		PayloadSize:  r.ContentLength,
		Predicted:    predictedClass,
		ActualRoute:  actualClass,
		ProcessingMs: processingMs,
		WorkerID:     workerID,
		Status:       crw.statusCode,
	})

	// ── Drift Detection (Class 0 requests only) ──
	if actualClass == 0 {
		rt.driftMonitor.Add(processingMs)
	}
}

// handleStatus returns the health and active cumulative in-flight weight status of all backends.
func (rt *Router) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	type backendStatus struct {
		URL            string `json:"url"`
		Healthy        bool   `json:"healthy"`
		Lane           string `json:"lane"`
		InFlightWeight int64  `json:"in_flight_weight"`
	}

	var statuses []backendStatus
	for _, b := range rt.fastLane {
		statuses = append(statuses, backendStatus{
			URL:            b.URL.String(),
			Healthy:        b.IsHealthy(),
			Lane:           "fast",
			InFlightWeight: b.InFlightWeight.Load(),
		})
	}
	for _, b := range rt.mediumLane {
		statuses = append(statuses, backendStatus{
			URL:            b.URL.String(),
			Healthy:        b.IsHealthy(),
			Lane:           "medium",
			InFlightWeight: b.InFlightWeight.Load(),
		})
	}
	for _, b := range rt.slowLane {
		statuses = append(statuses, backendStatus{
			URL:            b.URL.String(),
			Healthy:        b.IsHealthy(),
			Lane:           "slow",
			InFlightWeight: b.InFlightWeight.Load(),
		})
	}

	rt.historyMu.RLock()
	recent := make([]RecentRequest, len(rt.recentHistory))
	copy(recent, rt.recentHistory)
	rt.historyMu.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"smart_routing":   rt.smartMode,
		"backends":        statuses,
		"recent_requests": recent,
	})
}

// handleDashboardTrigger executes a local loopback work request through the router
func (rt *Router) handleDashboardTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	workType := r.URL.Query().Get("type")
	if workType == "" {
		workType = "light"
	}

	var payload []byte
	if workType == "light" {
		payload = []byte(`{"data":"dashboard-trigger-sha256-light-payload"}`)
	} else {
		payload = []byte(`{"data":"dashboard-trigger-heavy-payload"}`)
	}

	// Dynamic loopback call
	mockReq, err := http.NewRequest(http.MethodPost, "/work?type="+workType, bytes.NewBuffer(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mockReq.Header.Set("Content-Type", "application/json")
	mockReq.ContentLength = int64(len(payload))

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, mockReq)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

// handleDashboard serves the HTML dashboard page
func (rt *Router) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	htmlContent := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>NeuroRoute — AI-Routing Live Dashboard</title>
    <link href="https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;600;800&family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
    <style>
        :root {
            --bg-base: #0b0f19;
            --bg-surface: #131929;
            --border-glow: rgba(56, 189, 248, 0.15);
            --border-hover: rgba(56, 189, 248, 0.4);
            --text-primary: #f8fafc;
            --text-secondary: #94a3b8;
            --neon-green: #10b981;
            --neon-amber: #f59e0b;
            --neon-red: #ef4444;
            --neon-blue: #0ea5e9;
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            background-color: var(--bg-base);
            color: var(--text-primary);
            font-family: 'Outfit', sans-serif;
            min-height: 100vh;
            padding: 2rem;
            line-height: 1.5;
            background-image: radial-gradient(circle at 50% 0%, rgba(14, 165, 233, 0.12) 0%, transparent 60%);
        }

        .container {
            max-width: 1200px;
            margin: 0 auto;
            display: flex;
            flex-direction: column;
            gap: 2rem;
        }

        /* Header */
        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            border-bottom: 1px solid rgba(255, 255, 255, 0.08);
            padding-bottom: 1.5rem;
        }

        .logo-section h1 {
            font-size: 2.2rem;
            font-weight: 800;
            background: linear-gradient(to right, #38bdf8, #818cf8);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            letter-spacing: -0.03em;
        }

        .logo-section p {
            color: var(--text-secondary);
            font-size: 0.95rem;
            margin-top: 0.25rem;
        }

        .badge {
            background: rgba(16, 185, 129, 0.1);
            border: 1px solid rgba(16, 185, 129, 0.25);
            color: var(--neon-green);
            padding: 0.4rem 1rem;
            border-radius: 9999px;
            font-weight: 600;
            font-size: 0.85rem;
            display: flex;
            align-items: center;
            gap: 0.5rem;
            box-shadow: 0 0 15px rgba(16, 185, 129, 0.1);
        }

        .badge.pulse::before {
            content: '';
            width: 8px;
            height: 8px;
            background-color: var(--neon-green);
            border-radius: 50%;
            display: inline-block;
            animation: pulse-glow 1.5s infinite;
        }

        @keyframes pulse-glow {
            0% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(16, 185, 129, 0.7); }
            70% { transform: scale(1); box-shadow: 0 0 0 8px rgba(16, 185, 129, 0); }
            100% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(16, 185, 129, 0); }
        }

        /* Dashboard Grid */
        .grid {
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 2rem;
        }

        @media (max-width: 900px) {
            .grid {
                grid-template-columns: 1fr;
            }
        }

        /* Cards */
        .card {
            background: var(--bg-surface);
            border: 1px solid var(--border-glow);
            border-radius: 16px;
            padding: 1.8rem;
            backdrop-filter: blur(10px);
            box-shadow: 0 10px 30px rgba(0, 0, 0, 0.25);
            transition: border-color 0.3s ease, box-shadow 0.3s ease;
        }

        .card:hover {
            border-color: var(--border-hover);
            box-shadow: 0 10px 35px rgba(56, 189, 248, 0.05);
        }

        .card h2 {
            font-size: 1.3rem;
            font-weight: 600;
            margin-bottom: 1.5rem;
            border-left: 4px solid var(--neon-blue);
            padding-left: 0.75rem;
        }

        /* Lane Status Bars */
        .lane-card {
            display: flex;
            flex-direction: column;
            gap: 1.25rem;
        }

        .lane-item {
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid rgba(255, 255, 255, 0.05);
            border-radius: 12px;
            padding: 1rem 1.25rem;
            display: flex;
            flex-direction: column;
            gap: 0.5rem;
        }

        .lane-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        .lane-title {
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }

        .lane-indicator {
            width: 8px;
            height: 8px;
            border-radius: 50%;
        }

        .lane-indicator.healthy { background-color: var(--neon-green); box-shadow: 0 0 10px var(--neon-green); }
        .lane-indicator.down { background-color: var(--neon-red); box-shadow: 0 0 10px var(--neon-red); }

        .lane-meta {
            font-size: 0.85rem;
            color: var(--text-secondary);
            font-family: 'JetBrains Mono', monospace;
        }

        .weight-progress-container {
            width: 100%;
            height: 8px;
            background: rgba(255, 255, 255, 0.05);
            border-radius: 9999px;
            overflow: hidden;
            margin-top: 0.25rem;
        }

        .weight-progress {
            height: 100%;
            width: 0%;
            border-radius: 9999px;
            transition: width 0.5s cubic-bezier(0.4, 0, 0.2, 1);
        }

        .lane-item.fast .weight-progress { background-color: var(--neon-green); }
        .lane-item.medium .weight-progress { background-color: var(--neon-amber); }
        .lane-item.slow .weight-progress { background-color: var(--neon-red); }

        /* Playground Actions */
        .playground-grid {
            display: grid;
            grid-template-columns: repeat(3, 1fr);
            gap: 1rem;
            margin-bottom: 1.5rem;
        }

        .btn {
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid rgba(255, 255, 255, 0.08);
            border-radius: 12px;
            color: var(--text-primary);
            padding: 1.2rem 1rem;
            font-family: 'Outfit', sans-serif;
            font-weight: 600;
            font-size: 0.95rem;
            cursor: pointer;
            display: flex;
            flex-direction: column;
            align-items: center;
            gap: 0.5rem;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
        }

        .btn:hover {
            background: rgba(255, 255, 255, 0.05);
            transform: translateY(-2px);
        }

        .btn:active {
            transform: translateY(0);
        }

        .btn.light:hover { border-color: var(--neon-green); box-shadow: 0 0 15px rgba(16, 185, 129, 0.15); }
        .btn.medium:hover { border-color: var(--neon-amber); box-shadow: 0 0 15px rgba(245, 158, 11, 0.15); }
        .btn.heavy:hover { border-color: var(--neon-red); box-shadow: 0 0 15px rgba(239, 68, 68, 0.15); }

        .btn svg {
            width: 24px;
            height: 24px;
            transition: transform 0.3s ease;
        }

        .btn:hover svg {
            transform: scale(1.15);
        }

        .btn.light svg { stroke: var(--neon-green); }
        .btn.medium svg { stroke: var(--neon-amber); }
        .btn.heavy svg { stroke: var(--neon-red); }

        .console-output {
            background: rgba(0, 0, 0, 0.4);
            border: 1px solid rgba(255, 255, 255, 0.06);
            border-radius: 12px;
            padding: 1rem 1.25rem;
            font-family: 'JetBrains Mono', monospace;
            font-size: 0.85rem;
            min-height: 120px;
            max-height: 120px;
            overflow-y: auto;
            color: #38bdf8;
            display: flex;
            flex-direction: column;
            gap: 0.25rem;
        }

        /* Classification Stream Table */
        .table-container {
            width: 100%;
            overflow-x: auto;
        }

        table {
            width: 100%;
            border-collapse: collapse;
            text-align: left;
            font-size: 0.9rem;
        }

        th {
            color: var(--text-secondary);
            font-weight: 600;
            padding: 0.75rem 1rem;
            border-bottom: 2px solid rgba(255, 255, 255, 0.08);
            font-size: 0.8rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }

        td {
            padding: 0.85rem 1rem;
            border-bottom: 1px solid rgba(255, 255, 255, 0.04);
            font-family: 'Outfit', sans-serif;
        }

        tr:last-child td {
            border-bottom: none;
        }

        .mono {
            font-family: 'JetBrains Mono', monospace;
            font-size: 0.8rem;
        }

        .badge-class {
            display: inline-block;
            padding: 0.2rem 0.6rem;
            border-radius: 6px;
            font-size: 0.75rem;
            font-weight: 600;
            text-transform: uppercase;
        }

        .badge-class.light { background: rgba(16, 185, 129, 0.1); border: 1px solid rgba(16, 185, 129, 0.2); color: var(--neon-green); }
        .badge-class.medium { background: rgba(245, 158, 11, 0.1); border: 1px solid rgba(245, 158, 11, 0.2); color: var(--neon-amber); }
        .badge-class.heavy { background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.2); color: var(--neon-red); }

        .spinner {
            animation: rotate 2s linear infinite;
            width: 14px;
            height: 14px;
            margin-right: 0.5rem;
            display: inline-block;
        }

        @keyframes rotate {
            100% { transform: rotate(360deg); }
        }

        .spinner circle {
            stroke: currentColor;
            stroke-linecap: round;
            animation: dash 1.5s ease-in-out infinite;
        }

        @keyframes dash {
            0% { stroke-dasharray: 1, 150; stroke-dashoffset: 0; }
            50% { stroke-dasharray: 90, 150; stroke-dashoffset: -35; }
            100% { stroke-dasharray: 90, 150; stroke-dashoffset: -124; }
        }
    </style>
</head>
<body>
    <div class="container">
        <!-- Header -->
        <header>
            <div class="logo-section">
                <h1>NEUROROUTE</h1>
                <p>AI-Driven Predictive Layer 7 Gateway</p>
            </div>
            <div class="badge pulse" id="routing-mode">SMART ROUTING ACTIVE</div>
        </header>

        <div class="grid">
            <!-- Left Side: Lane Status -->
            <div class="card">
                <h2>L7 Concurrency Lane Status</h2>
                <div class="lane-card" id="lanes-container">
                    <!-- Fast Lane -->
                    <div class="lane-item fast">
                        <div class="lane-header">
                            <div class="lane-title">
                                <div class="lane-indicator healthy" id="indicator-fast"></div>
                                <span>Fast Lane (Class 0)</span>
                            </div>
                            <div class="lane-meta" id="weight-fast">Weight: 0</div>
                        </div>
                        <div class="weight-progress-container">
                            <div class="weight-progress" id="progress-fast" style="width: 0%;"></div>
                        </div>
                        <div class="lane-meta" id="url-fast">Url: loading...</div>
                    </div>

                    <!-- Medium Lane -->
                    <div class="lane-item medium">
                        <div class="lane-header">
                            <div class="lane-title">
                                <div class="lane-indicator healthy" id="indicator-medium"></div>
                                <span>Medium Lane (Class 1)</span>
                            </div>
                            <div class="lane-meta" id="weight-medium">Weight: 0</div>
                        </div>
                        <div class="weight-progress-container">
                            <div class="weight-progress" id="progress-medium" style="width: 0%;"></div>
                        </div>
                        <div class="lane-meta" id="url-medium">Url: loading...</div>
                    </div>

                    <!-- Slow Lane -->
                    <div class="lane-item slow">
                        <div class="lane-header">
                            <div class="lane-title">
                                <div class="lane-indicator healthy" id="indicator-slow"></div>
                                <span>Slow Lane (Class 2)</span>
                            </div>
                            <div class="lane-meta" id="weight-slow">Weight: 0</div>
                        </div>
                        <div class="weight-progress-container">
                            <div class="weight-progress" id="progress-slow" style="width: 0%;"></div>
                        </div>
                        <div class="lane-meta" id="url-slow">Url: loading...</div>
                    </div>
                </div>
            </div>

            <!-- Right Side: Interactive Playground -->
            <div class="card">
                <h2>Interactive Traffic Playground</h2>
                <div class="playground-grid">
                    <button class="btn light" onclick="triggerLoad('light')">
                        <svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <path d="M13 2L3 14h9l-1 8 10-16h-9l1-8z"/>
                        </svg>
                        <span>Trigger Light</span>
                    </button>
                    <button class="btn medium" onclick="triggerLoad('medium')">
                        <svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <circle cx="12" cy="12" r="10"/>
                            <path d="M12 6v6l4 2"/>
                        </svg>
                        <span>Trigger Medium</span>
                    </button>
                    <button class="btn heavy" onclick="triggerLoad('heavy')">
                        <svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <rect x="2" y="2" width="20" height="8" rx="2" ry="2"/>
                            <rect x="2" y="14" width="20" height="8" rx="2" ry="2"/>
                            <line x1="6" y1="6" x2="6.01" y2="6"/>
                            <line x1="6" y1="18" x2="6.01" y2="18"/>
                        </svg>
                        <span>Trigger Heavy</span>
                    </button>
                </div>
                <div style="margin-top: 1.25rem; margin-bottom: 1.25rem; display: flex; align-items: center; justify-content: space-between; border-top: 1px solid rgba(255,255,255,0.06); padding-top: 1rem;">
                    <div style="font-size: 0.85rem; color: var(--text-secondary); text-align: left;">
                        <strong style="color: var(--neon-blue);">Continuous Load Simulator:</strong><br/>
                        <span style="font-size: 0.75rem;">Spawns dynamic overlapping traffic (70% Light, 20% Medium, 10% Heavy) every 400ms to sustain queues.</span>
                    </div>
                    <button class="btn" id="btn-simulate" onclick="toggleSimulation()" style="flex-direction: row; padding: 0.6rem 1rem; font-size: 0.85rem; border-color: var(--neon-blue); box-shadow: 0 0 10px rgba(14, 165, 233, 0.1); width: auto; margin: 0;">
                        <svg viewBox="0 0 24 24" fill="none" stroke="var(--neon-blue)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="width: 16px; height: 16px; margin-right: 0.4rem;" id="simulate-icon">
                            <polygon points="5 3 19 12 5 21 5 3"/>
                        </svg>
                        <span id="simulate-text">Start Simulation</span>
                    </button>
                </div>
                <div class="console-output" id="console">
                    <div>[SYSTEM] Ready for operations. Click a button to test load-balancing routing...</div>
                </div>
            </div>
        </div>

        <!-- Live SLA & HOL Analyzer Card -->
        <div class="card" id="sla-card" style="border-color: var(--neon-blue); box-shadow: 0 0 20px rgba(14, 165, 233, 0.05);">
            <h2>Live Performance & SLA Analyzer</h2>
            <div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 1.5rem; margin-bottom: 1.2rem;">
                <!-- Metric 1: Avg Light Latency -->
                <div style="background: rgba(255, 255, 255, 0.02); border: 1px solid rgba(255, 255, 255, 0.05); border-radius: 12px; padding: 1rem; text-align: center;">
                    <div style="font-size: 0.85rem; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Avg Light Latency</div>
                    <div style="font-size: 2rem; font-weight: 800; color: var(--neon-green); margin: 0.5rem 0;" id="avg-light-latency">0.0ms</div>
                    <div style="font-size: 0.75rem; color: var(--text-secondary);" id="light-latency-status">Healthy (&lt; 15ms)</div>
                </div>

                <!-- Metric 2: Avg Heavy Latency -->
                <div style="background: rgba(255, 255, 255, 0.02); border: 1px solid rgba(255, 255, 255, 0.05); border-radius: 12px; padding: 1rem; text-align: center;">
                    <div style="font-size: 0.85rem; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">Avg Heavy Latency</div>
                    <div style="font-size: 2rem; font-weight: 800; color: var(--neon-amber); margin: 0.5rem 0;" id="avg-heavy-latency">0.0ms</div>
                    <div style="font-size: 0.75rem; color: var(--text-secondary);">CPU-Bound Processing</div>
                </div>

                <!-- Metric 3: HOL Blocking Index -->
                <div style="background: rgba(255, 255, 255, 0.02); border: 1px solid rgba(255, 255, 255, 0.05); border-radius: 12px; padding: 1rem; text-align: center; display: flex; flex-direction: column; justify-content: center; align-items: center; border: 1px solid rgba(255,255,255,0.05);" id="hol-box">
                    <div style="font-size: 0.85rem; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em;">HOL Blocking Index</div>
                    <div style="font-size: 1.6rem; font-weight: 800; color: var(--neon-green); margin: 0.5rem 0;" id="hol-value">0%</div>
                    <div style="font-size: 0.75rem; color: var(--text-secondary);" id="hol-desc">Lanes are fully isolated</div>
                </div>
            </div>
            <!-- Live Analysis Explainer Text -->
            <div style="background: rgba(0, 0, 0, 0.2); border-left: 4px solid var(--neon-blue); padding: 1rem; border-radius: 4px; font-size: 0.92rem; line-height: 1.6;" id="analysis-explainer">
                Analyzing traffic pattern. Trigger some requests or start the simulator to begin evaluation...
            </div>
        </div>

        <!-- Classification Log Stream -->
        <div class="card">
            <h2>Real-Time Classification Log Stream</h2>
            <div class="table-container">
                <table>
                    <thead>
                        <tr>
                            <th>Time</th>
                            <th>Method & Path</th>
                            <th>Worker Node</th>
                            <th>Prediction</th>
                            <th>Actual Route</th>
                            <th>Latency</th>
                            <th>Status</th>
                        </tr>
                    </thead>
                    <tbody id="logs-body">
                        <tr>
                            <td colspan="7" style="text-align: center; color: var(--text-secondary);">No incoming requests logged yet. Trigger some traffic above!</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>

    <script>
        var API_BASE = window.location.origin;
        var simulationInterval = null;

        function toggleSimulation() {
            var btn = document.getElementById('btn-simulate');
            var text = document.getElementById('simulate-text');
            var icon = document.getElementById('simulate-icon');

            if (simulationInterval) {
                clearInterval(simulationInterval);
                simulationInterval = null;
                text.textContent = 'Start Simulation';
                btn.style.borderColor = 'var(--neon-blue)';
                btn.style.boxShadow = '0 0 10px rgba(14, 165, 233, 0.1)';
                icon.innerHTML = '<polygon points="5 3 19 12 5 21 5 3"/>';
                icon.setAttribute('stroke', 'var(--neon-blue)');
                logToConsole('[SIMULATOR] Live traffic stream simulation paused.');
            } else {
                logToConsole('[SIMULATOR] Starting sustained concurrent traffic simulation stream...');
                icon.innerHTML = '<rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/>';
                icon.setAttribute('stroke', 'var(--neon-red)');
                text.textContent = 'Pause Simulation';
                btn.style.borderColor = 'var(--neon-red)';
                btn.style.boxShadow = '0 0 15px rgba(239, 68, 68, 0.2)';

                simulationInterval = setInterval(function() {
                    var rand = Math.random();
                    var type = 'light';
                    if (rand > 0.5 && rand <= 0.7) {
                        type = 'heavy';
                    } else if (rand > 0.7) {
                        type = 'matrix';
                    }
                    triggerLoad(type);
                }, 150);
            }
        }

        function logToConsole(text, isError) {
            var consoleBox = document.getElementById('console');
            var entry = document.createElement('div');
            entry.style.color = isError ? 'var(--neon-red)' : '#38bdf8';
            entry.textContent = '[' + new Date().toLocaleTimeString() + '] ' + text;
            consoleBox.appendChild(entry);
            consoleBox.scrollTop = consoleBox.scrollHeight;
        }

        async function triggerLoad(type) {
            logToConsole('Dispatching loopback POST /work?type=' + type + ' to L7 gateway...');
            var start = performance.now();
            try {
                var res = await fetch(API_BASE + '/dashboard/trigger?type=' + type, { method: 'POST' });
                var data = await res.json();
                var elapsed = (performance.now() - start).toFixed(1);
                if (res.ok) {
                    logToConsole('✅ Success! Routed to ' + data.worker_id + ' in ' + elapsed + 'ms.');
                    updateStatus();
                } else {
                    logToConsole('❌ Server error: ' + (data.error || 'unknown failure'), true);
                }
            } catch (err) {
                logToConsole('❌ Network error while dispatching job: ' + err.message, true);
            }
        }

        async function updateStatus() {
            try {
                var res = await fetch(API_BASE + '/status');
                if (!res.ok) return;
                var data = await res.json();

                // Update Routing Mode Badge
                var modeBadge = document.getElementById('routing-mode');
                if (data.smart_routing) {
                    modeBadge.textContent = 'SMART ROUTING ACTIVE';
                    modeBadge.className = 'badge pulse';
                    modeBadge.style.color = 'var(--neon-green)';
                    modeBadge.style.background = 'rgba(16, 185, 129, 0.1)';
                    modeBadge.style.borderColor = 'rgba(16, 185, 129, 0.25)';
                } else {
                    modeBadge.textContent = 'SMART ROUTING BYPASSED (DUMB)';
                    modeBadge.className = 'badge';
                    modeBadge.style.color = 'var(--neon-amber)';
                    modeBadge.style.background = 'rgba(245, 158, 11, 0.1)';
                    modeBadge.style.borderColor = 'rgba(245, 158, 11, 0.25)';
                }

                // Update Lanes status
                var maxWeight = 100;
                data.backends.forEach(function(b) {
                    var suffix = 'fast';
                    if (b.lane === 'medium') suffix = 'medium';
                    if (b.lane === 'slow') suffix = 'slow';

                    var indicator = document.getElementById('indicator-' + suffix);
                    if (b.healthy) {
                        indicator.className = 'lane-indicator healthy';
                    } else {
                        indicator.className = 'lane-indicator down';
                    }

                    document.getElementById('weight-' + suffix).textContent = 'In-Flight Weight: ' + b.in_flight_weight;
                    document.getElementById('url-' + suffix).textContent = 'Backend: ' + b.url;

                    // Update progress bar width
                    var percentage = Math.min(100, (b.in_flight_weight / maxWeight) * 100);
                    if (b.in_flight_weight > 0 && percentage < 5) percentage = 5;
                    document.getElementById('progress-' + suffix).style.width = percentage + '%';
                });

                // Calculate SLA metrics from recent requests
                if (data.recent_requests && data.recent_requests.length > 0) {
                    var lightRequests = data.recent_requests.filter(function(r) { return r.actual_route === 0 || r.predicted === 0; });
                    var heavyRequests = data.recent_requests.filter(function(r) { return r.actual_route === 2 || r.predicted === 2; });

                    var avgLight = 0;
                    if (lightRequests.length > 0) {
                        var sumLight = 0;
                        lightRequests.forEach(function(r) { sumLight += r.processing_ms; });
                        avgLight = sumLight / lightRequests.length;
                    }

                    var avgHeavy = 0;
                    if (heavyRequests.length > 0) {
                        var sumHeavy = 0;
                        heavyRequests.forEach(function(r) { sumHeavy += r.processing_ms; });
                        avgHeavy = sumHeavy / heavyRequests.length;
                    }

                    // Calculate HOL Blocking Index (% of light requests taking > 50ms)
                    var holCount = 0;
                    lightRequests.forEach(function(r) {
                        if (r.processing_ms > 50) holCount++;
                    });
                    var holIndex = lightRequests.length > 0 ? Math.round((holCount / lightRequests.length) * 100) : 0;

                    // Update UI elements
                    document.getElementById('avg-light-latency').textContent = avgLight.toFixed(1) + 'ms';
                    document.getElementById('avg-heavy-latency').textContent = avgHeavy.toFixed(1) + 'ms';
                    
                    var lightStatus = document.getElementById('light-latency-status');
                    var lightVal = document.getElementById('avg-light-latency');
                    if (avgLight < 15) {
                        lightVal.style.color = 'var(--neon-green)';
                        lightStatus.textContent = 'Healthy (< 15ms)';
                    } else if (avgLight >= 15 && avgLight < 50) {
                        lightVal.style.color = 'var(--neon-amber)';
                        lightStatus.textContent = 'Warning (Mild Delay)';
                    } else {
                        lightVal.style.color = 'var(--neon-red)';
                        lightStatus.textContent = 'Critical (HOL Queue Blocked)';
                    }

                    var holValue = document.getElementById('hol-value');
                    var holDesc = document.getElementById('hol-desc');
                    var holBox = document.getElementById('hol-box');
                    var explainer = document.getElementById('analysis-explainer');

                    holValue.textContent = holIndex + '%';
                    if (holIndex === 0) {
                        holValue.style.color = 'var(--neon-green)';
                        holDesc.textContent = 'Lanes are fully isolated';
                        holBox.style.borderColor = 'rgba(16, 185, 129, 0.2)';
                        
                        if (data.smart_routing) {
                            explainer.innerHTML = '🟢 <strong>Smart Lane Isolation Active:</strong> Heavy computations are isolated in the Slow Lane. Light tasks are executing instantly in the Fast Lane without any queuing delays. Your SLA is 100% guaranteed!';
                            explainer.style.borderLeftColor = 'var(--neon-green)';
                        } else {
                            explainer.innerHTML = '🟡 <strong>Baseline Active (Idle):</strong> Currently no active HOL blocking because traffic is low. Trigger a heavy task or start simulation to see queue degradation.';
                            explainer.style.borderLeftColor = 'var(--neon-amber)';
                        }
                    } else if (holIndex > 0 && holIndex <= 35) {
                        holValue.style.color = 'var(--neon-amber)';
                        holDesc.textContent = 'Mild Head-of-Line Blocking';
                        holBox.style.borderColor = 'rgba(245, 158, 11, 0.3)';
                        explainer.innerHTML = '⚠️ <strong>Warning: Mild Head-of-Line Blocking Detected!</strong> Some light requests are starting to experience latency delays because they are queued behind heavier computations.';
                        explainer.style.borderLeftColor = 'var(--neon-amber)';
                    } else {
                        holValue.style.color = 'var(--neon-red)';
                        holDesc.textContent = 'CRITICAL HOL BLOCKING!';
                        holBox.style.borderColor = 'rgba(239, 68, 68, 0.5)';
                        explainer.innerHTML = '🚨 <strong>Critical Head-of-Line Blocking!</strong> Shared queues have collapsed. Over ' + holIndex + '% of fast light requests are severely delayed by heavy matrix computations. This is the exact failure model that NeuroRoute solves!';
                        explainer.style.borderLeftColor = 'var(--neon-red)';
                    }
                }

                // Update logs body
                var logsBody = document.getElementById('logs-body');
                if (data.recent_requests && data.recent_requests.length > 0) {
                    logsBody.innerHTML = '';
                    data.recent_requests.forEach(function(r) {
                        var tr = document.createElement('tr');

                        var classLabels = ['light', 'medium', 'heavy'];
                        var predictedLabel = classLabels[r.predicted] || 'unknown';
                        var actualLabel = classLabels[r.actual_route] || 'unknown';

                        var statusStyle = r.status >= 200 && r.status < 300 ? 'color: var(--neon-green)' : 'color: var(--neon-red)';

                        tr.innerHTML = '<td class="mono">' + r.timestamp + '</td>' +
                            '<td class="mono"><strong>' + r.method + '</strong> ' + r.path + '</td>' +
                            '<td class="mono">' + r.worker_id + '</td>' +
                            '<td><span class="badge-class ' + predictedLabel + '">' + predictedLabel + '</span></td>' +
                            '<td><span class="badge-class ' + actualLabel + '">' + actualLabel + '</span></td>' +
                            '<td class="mono">' + r.processing_ms.toFixed(2) + 'ms</td>' +
                            '<td class="mono" style="' + statusStyle + '; font-weight: bold;">' + r.status + '</td>';
                        logsBody.appendChild(tr);
                    });
                }
            } catch (err) {
                console.error('Error fetching dashboard status:', err);
            }
        }

        // Auto-update every 250 milliseconds
        setInterval(updateStatus, 250);
        updateStatus(); // Initial call
    </script>
</body>
</html>`

	w.Write([]byte(htmlContent))
}

// ── Response Writer Wrapper ─────────────────────────────────

type captureResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (crw *captureResponseWriter) WriteHeader(code int) {
	crw.statusCode = code
	crw.written = true
	crw.ResponseWriter.WriteHeader(code)
}

func (crw *captureResponseWriter) Write(b []byte) (int, error) {
	if !crw.written {
		crw.statusCode = http.StatusOK
		crw.written = true
	}
	return crw.ResponseWriter.Write(b)
}

func (crw *captureResponseWriter) Flush() {
	if flusher, ok := crw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ── Health Check Goroutine ──────────────────────────────────

func startHealthChecker(backends []*Backend, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 10 * time.Second}

	for range ticker.C {
		for _, b := range backends {
			go func(backend *Backend) {
				resp, err := client.Get(backend.URL.String() + "/ping")
				if err != nil {
					backend.MarkDown()
					return
				}
				defer resp.Body.Close()
				io.ReadAll(resp.Body)

				// Accept any response status code < 500 as a sign that the backend is up and reachable
				if resp.StatusCode < 500 {
					backend.MarkUp()
				} else {
					backend.MarkDown()
				}
			}(b)
		}
	}
}

// ── Main ────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()

	log.Printf("🧠 NeuroRoute Gateway starting on :%s", cfg.Port)
	log.Printf("   Smart routing: %v", cfg.SmartRouting)
	log.Printf("   Fast lane workers: %v", cfg.FastWorkerURLs)
	log.Printf("   Medium lane workers: %v", cfg.MediumWorkerURLs)
	log.Printf("   Slow lane workers: %v", cfg.SlowWorkerURLs)
	log.Printf("   Traffic log: %s", cfg.TrafficLogPath)

	router, err := NewRouter(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to initialize router: %v", err)
	}

	allBackends := make([]*Backend, 0, len(router.fastLane)+len(router.mediumLane)+len(router.slowLane))
	allBackends = append(allBackends, router.fastLane...)
	allBackends = append(allBackends, router.mediumLane...)
	allBackends = append(allBackends, router.slowLane...)

	// Start background health checker (every 10 seconds)
	go startHealthChecker(allBackends, 10*time.Second)

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("❌ Gateway failed to start: %v", err)
	}
}
