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

type Router struct {
	fastLane     []*Backend // Class 0
	mediumLane   []*Backend // Class 1
	slowLane     []*Backend // Class 2
	logger       *TrafficLogger
	driftMonitor *DriftMonitor
	smartMode    bool
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
		fastLane:     fastLane,
		mediumLane:   mediumLane,
		slowLane:     slowLane,
		logger:       logger,
		driftMonitor: NewDriftMonitor(),
		smartMode:    cfg.SmartRouting,
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

	json.NewEncoder(w).Encode(map[string]interface{}{
		"smart_routing": rt.smartMode,
		"backends":      statuses,
	})
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

	client := &http.Client{Timeout: 2 * time.Second}

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
