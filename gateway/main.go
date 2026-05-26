// ──────────────────────────────────────────────────────────────
//  NeuroRoute — AI-Driven Reverse Proxy Gateway
//
//  A production-grade Layer 7 reverse proxy that:
//    1. Intercepts incoming HTTP requests
//    2. Extracts request metadata (path, method, size, params)
//    3. Queries the Python ML service for a prediction
//    4. Routes light requests (label=0) round-robin to workers 1-3
//    5. Routes heavy requests (label=1) directly to worker 4
//
//  Includes:
//    • Async CSV traffic logging (zero request-path impact)
//    • Connection-pooled HTTP client for ML queries
//    • 30-second circuit breaker for downed workers
//    • Graceful degradation to round-robin on ML failure
// ──────────────────────────────────────────────────────────────

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	WorkerURLs       []string
	HeavyWorkerURLs  []string
	MLServiceURL     string
	MLTimeoutMs      int
	SmartRouting     bool
	TrafficLogPath   string
	Port             string
}

func loadConfig() Config {
	cfg := Config{
		MLServiceURL:   getEnv("ML_SERVICE_URL", "http://ml_service:8050"),
		TrafficLogPath: getEnv("TRAFFIC_LOG_PATH", "/data/traffic.csv"),
		Port:           "8000",
	}

	// Parse comma-separated worker URLs
	workerStr := getEnv("WORKER_URLS", "http://worker_1:8001,http://worker_2:8002,http://worker_3:8003")
	cfg.WorkerURLs = strings.Split(workerStr, ",")

	// Parse comma-separated heavy worker URLs
	heavyStr := getEnv("HEAVY_WORKER_URLS", getEnv("HEAVY_WORKER_URL", "http://worker_4:8004"))
	cfg.HeavyWorkerURLs = strings.Split(heavyStr, ",")

	// Parse ML timeout
	timeoutStr := getEnv("ML_PREDICT_TIMEOUT_MS", "5")
	timeout, err := strconv.Atoi(timeoutStr)
	if err != nil {
		timeout = 5
	}
	cfg.MLTimeoutMs = timeout

	// Parse smart routing flag
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
	URL       *url.URL
	Proxy     *httputil.ReverseProxy
	Healthy   bool
	DownSince time.Time
	mu        sync.RWMutex
}

func NewBackend(rawURL string) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid worker URL %q: %w", rawURL, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)

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
			// Attempt recovery (will be confirmed on next request)
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
	Timestamp      float64
	URLPath        string
	HTTPMethod     string
	ContentLength  int64
	QueryParams    string
	ProcessingMs   float64
	WorkerID       string
}

type TrafficLogger struct {
	ch     chan TrafficLog
	writer *csv.Writer
	file   *os.File
}

func NewTrafficLogger(path string) (*TrafficLogger, error) {
	// Ensure parent directory exists
	if dir := path[:strings.LastIndex(path, "/")]; dir != "" {
		os.MkdirAll(dir, 0755)
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
	// Non-blocking send — drop log if channel full (never block requests)
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
				fmt.Sprintf("%.3f", entry.ProcessingMs),
				entry.WorkerID,
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

// ── ML Client ───────────────────────────────────────────────

type MLPrediction struct {
	Label int `json:"label"`
}

type MLRequest struct {
	URLPath       string `json:"url_path"`
	Method        string `json:"method"`
	ContentLength int64  `json:"content_length"`
	IsHeavyQuery  int    `json:"is_heavy_query"`
}

type MLClient struct {
	serviceURL string
	client     *http.Client
}

func NewMLClient(serviceURL string, timeoutMs int) *MLClient {
	return &MLClient{
		serviceURL: serviceURL,
		client: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
			},
		},
	}
}

func (ml *MLClient) Predict(urlPath, method string, contentLength int64, queryParams string) (int, error) {
	// Extract whether query params indicate heavy work
	isHeavy := 0
	if strings.Contains(queryParams, "heavy") || strings.Contains(queryParams, "matrix") {
		isHeavy = 1
	}

	reqBody := MLRequest{
		URLPath:       urlPath,
		Method:        method,
		ContentLength: contentLength,
		IsHeavyQuery:  isHeavy,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshal error: %w", err)
	}

	resp, err := ml.client.Post(
		ml.serviceURL+"/predict",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return 0, fmt.Errorf("ML service error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("ML service returned %d", resp.StatusCode)
	}

	var pred MLPrediction
	if err := json.NewDecoder(resp.Body).Decode(&pred); err != nil {
		return 0, fmt.Errorf("decode error: %w", err)
	}

	return pred.Label, nil
}

// ── Router ──────────────────────────────────────────────────

type Router struct {
	fastLane       []*Backend  // workers 1-3 (light requests)
	slowLane       []*Backend  // workers 4-5 (heavy requests)
	rrCounter      atomic.Uint64
	slowRRCounter  atomic.Uint64
	mlClient       *MLClient
	logger         *TrafficLogger
	smartMode      bool
}

func NewRouter(cfg Config) (*Router, error) {
	// Initialize fast lane backends
	var fastLane []*Backend
	for _, u := range cfg.WorkerURLs {
		b, err := NewBackend(u)
		if err != nil {
			return nil, err
		}
		fastLane = append(fastLane, b)
	}

	// Initialize slow lane backends
	var slowLane []*Backend
	for _, u := range cfg.HeavyWorkerURLs {
		b, err := NewBackend(u)
		if err != nil {
			return nil, err
		}
		slowLane = append(slowLane, b)
	}

	// Initialize traffic logger
	logger, err := NewTrafficLogger(cfg.TrafficLogPath)
	if err != nil {
		return nil, err
	}

	// Initialize ML client
	mlClient := NewMLClient(cfg.MLServiceURL, cfg.MLTimeoutMs)

	return &Router{
		fastLane:  fastLane,
		slowLane:  slowLane,
		mlClient:  mlClient,
		logger:    logger,
		smartMode: cfg.SmartRouting,
	}, nil
}

// nextFastLane returns the next healthy backend in round-robin order.
// If no healthy backend is available, returns nil.
func (rt *Router) nextFastLane() *Backend {
	total := len(rt.fastLane)
	for i := 0; i < total; i++ {
		idx := int(rt.rrCounter.Add(1)-1) % total
		b := rt.fastLane[idx]
		if b.IsHealthy() {
			return b
		}
	}
	return nil
}

// nextSlowLane returns the next healthy slow-lane backend in round-robin order.
// If no healthy backend is available, returns nil.
func (rt *Router) nextSlowLane() *Backend {
	total := len(rt.slowLane)
	for i := 0; i < total; i++ {
		idx := int(rt.slowRRCounter.Add(1)-1) % total
		b := rt.slowLane[idx]
		if b.IsHealthy() {
			return b
		}
	}
	return nil
}

// ServeHTTP is the main request handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// ── Direct endpoints on the gateway itself ──
	if r.URL.Path == "/ping" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "ok",
			"service":      "neuroroute-gateway",
			"smart_routing": rt.smartMode,
		})
		return
	}

	if r.URL.Path == "/status" {
		rt.handleStatus(w, r)
		return
	}

	// ── Determine target backend ──
	var target *Backend
	var label int
	queryParams := r.URL.RawQuery

	if rt.smartMode {
		// Call ML service for prediction
		predicted, err := rt.mlClient.Predict(
			r.URL.Path,
			r.Method,
			r.ContentLength,
			queryParams,
		)
		if err != nil {
			// Fail-safe: default to fast lane on ML failure
			log.Printf("⚠️  ML prediction failed (defaulting to fast lane): %v", err)
			label = 0
		} else {
			label = predicted
		}
	} else {
		// Dumb mode: always round-robin to fast lane
		label = 0
	}

	// ── Route based on label ──
	if label == 1 {
		// Heavy → slow lane (round-robin across heavy workers)
		target = rt.nextSlowLane()
		if target == nil {
			// Fallback: if all heavy workers are down, use fast lane
			log.Println("⚠️  All slow lane workers down, falling back to fast lane")
			target = rt.nextFastLane()
		}
	} else {
		// Light → fast lane (round-robin workers 1-3)
		target = rt.nextFastLane()
	}

	if target == nil {
		http.Error(w, `{"error": "no healthy backends available"}`, http.StatusServiceUnavailable)
		return
	}

	// ── Custom response writer to capture worker ID and status ──
	crw := &captureResponseWriter{ResponseWriter: w}

	// ── Proxy the request ──
	target.Proxy.ServeHTTP(crw, r)

	// ── Check if proxy succeeded ──
	// Note: We rely entirely on the active background health checker (which pings `/ping`)
	// to mark backends DOWN. Marking them down here during active heavy load because of client timeouts
	// causes cascading failures that spill heavy traffic into the fast lane.
	if crw.statusCode >= 502 {
		// Log proxy issues for debug but keep worker in pool unless health checker flags it
		log.Printf("⚠️  Proxy returned status %d for backend %s", crw.statusCode, target.URL.Host)
	}

	// ── Log traffic asynchronously ──
	elapsed := time.Since(start)
	workerID := crw.Header().Get("X-Worker-ID")
	if workerID == "" {
		workerID = target.URL.Host
	}

	rt.logger.Log(TrafficLog{
		Timestamp:     float64(start.UnixNano()) / 1e9,
		URLPath:       r.URL.Path,
		HTTPMethod:    r.Method,
		ContentLength: r.ContentLength,
		QueryParams:   queryParams,
		ProcessingMs:  float64(elapsed.Nanoseconds()) / 1e6,
		WorkerID:      workerID,
	})
}

// handleStatus returns the health status of all backends.
func (rt *Router) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	type backendStatus struct {
		URL     string `json:"url"`
		Healthy bool   `json:"healthy"`
		Lane    string `json:"lane"`
	}

	var statuses []backendStatus
	for _, b := range rt.fastLane {
		statuses = append(statuses, backendStatus{
			URL:     b.URL.String(),
			Healthy: b.IsHealthy(),
			Lane:    "fast",
		})
	}
	for _, b := range rt.slowLane {
		statuses = append(statuses, backendStatus{
			URL:     b.URL.String(),
			Healthy: b.IsHealthy(),
			Lane:    "slow",
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"smart_routing": rt.smartMode,
		"backends":      statuses,
	})
}

// ── Response Writer Wrapper ─────────────────────────────────

// captureResponseWriter wraps http.ResponseWriter to capture
// the status code written by the reverse proxy.
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

// Implement http.Flusher for streaming support
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

				if resp.StatusCode == http.StatusOK {
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
	log.Printf("   Fast lane workers: %v", cfg.WorkerURLs)
	log.Printf("   Slow lane workers: %v", cfg.HeavyWorkerURLs)
	log.Printf("   ML service: %s", cfg.MLServiceURL)
	log.Printf("   ML timeout: %dms", cfg.MLTimeoutMs)
	log.Printf("   Traffic log: %s", cfg.TrafficLogPath)

	router, err := NewRouter(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to initialize router: %v", err)
	}

	// Collect all backends for health checking
	allBackends := make([]*Backend, 0, len(router.fastLane)+len(router.slowLane))
	allBackends = append(allBackends, router.fastLane...)
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
