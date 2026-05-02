// ──────────────────────────────────────────────────────────────
//  NeuroRoute — Worker Microservice
//
//  A single binary serving CPU-bound tasks, parameterized by
//  the WORKER_ID environment variable. Each instance handles
//  light and heavy request types with real computation — no
//  fake sleeps.
//
//  Endpoints:
//    GET  /ping            → instant health check
//    POST /work?type=light → SHA-256 hash × 1000
//    POST /work?type=heavy → Sieve of Eratosthenes (N=50000)
//    POST /work?type=matrix→ 500×500 matrix multiplication
//    GET  /metrics         → active connections, avg latency
// ──────────────────────────────────────────────────────────────

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ── Configuration ───────────────────────────────────────────

var (
	workerID string
	port     string
)

func init() {
	workerID = os.Getenv("WORKER_ID")
	if workerID == "" {
		workerID = "0"
	}
	port = os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
}

// ── Metrics Tracking ────────────────────────────────────────

type Metrics struct {
	activeConns   atomic.Int64
	totalRequests atomic.Int64
	totalLatencyNs atomic.Int64 // cumulative nanoseconds
	mu            sync.RWMutex
}

var metrics Metrics

func (m *Metrics) trackRequest(start time.Time) {
	elapsed := time.Since(start)
	m.totalRequests.Add(1)
	m.totalLatencyNs.Add(int64(elapsed))
	m.activeConns.Add(-1)
}

// ── Handlers ────────────────────────────────────────────────

// handlePing returns an instant health check response.
func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Worker-ID", workerID)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"worker_id": workerID,
	})
}

// handleWork dispatches to the appropriate CPU-bound task
// based on the `type` query parameter.
func handleWork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	metrics.activeConns.Add(1)
	start := time.Now()
	defer metrics.trackRequest(start)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Worker-ID", workerID)

	workType := r.URL.Query().Get("type")

	switch workType {
	case "light":
		handleLightWork(w, r)
	case "heavy":
		handleHeavyWork(w, r)
	case "matrix":
		handleMatrixWork(w, r)
	default:
		// Default to light work if type is unspecified
		handleLightWork(w, r)
	}
}

// handleLightWork reads the request body and hashes it with
// SHA-256 a total of 1000 times. This is a fast CPU-bound task.
func handleLightWork(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error": "failed to read body"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// If body is empty, use a default payload
	if len(body) == 0 {
		body = []byte("neuroroute-default-payload")
	}

	// Hash 1000 times
	hash := body
	for i := 0; i < 1000; i++ {
		h := sha256.Sum256(hash)
		hash = h[:]
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"worker_id":  workerID,
		"task":       "light",
		"iterations": 1000,
		"hash":       fmt.Sprintf("%x", hash),
	})
}

// handleHeavyWork computes all prime numbers up to N=50000 using
// the Sieve of Eratosthenes. This is a CPU-intensive task that
// generates realistic heavy processing latency.
func handleHeavyWork(w http.ResponseWriter, r *http.Request) {
	const N = 50000

	// Sieve of Eratosthenes
	sieve := make([]bool, N+1)
	for i := 2; i <= N; i++ {
		sieve[i] = true
	}

	for i := 2; i*i <= N; i++ {
		if sieve[i] {
			for j := i * i; j <= N; j += i {
				sieve[j] = false
			}
		}
	}

	// Count primes
	count := 0
	var lastPrime int
	for i := 2; i <= N; i++ {
		if sieve[i] {
			count++
			lastPrime = i
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"worker_id":   workerID,
		"task":        "heavy",
		"algorithm":   "sieve_of_eratosthenes",
		"n":           N,
		"prime_count": count,
		"last_prime":  lastPrime,
	})
}

// handleMatrixWork multiplies two 500×500 floating-point matrices.
// This establishes a distinct heavy computational signature from
// the sieve task.
func handleMatrixWork(w http.ResponseWriter, r *http.Request) {
	const size = 500

	// Initialize matrices with deterministic pseudo-random values
	src := rand.NewSource(42)
	rng := rand.New(src)

	a := make([][]float64, size)
	b := make([][]float64, size)
	c := make([][]float64, size) // result

	for i := 0; i < size; i++ {
		a[i] = make([]float64, size)
		b[i] = make([]float64, size)
		c[i] = make([]float64, size)
		for j := 0; j < size; j++ {
			a[i][j] = rng.Float64()
			b[i][j] = rng.Float64()
		}
	}

	// Matrix multiplication: C = A × B
	for i := 0; i < size; i++ {
		for k := 0; k < size; k++ {
			aik := a[i][k]
			for j := 0; j < size; j++ {
				c[i][j] += aik * b[k][j]
			}
		}
	}

	// Compute a checksum (trace of result matrix)
	trace := 0.0
	for i := 0; i < size; i++ {
		trace += c[i][i]
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"worker_id": workerID,
		"task":      "matrix",
		"size":      fmt.Sprintf("%dx%d", size, size),
		"trace":     math.Round(trace*1000) / 1000,
	})
}

// handleMetrics returns current worker metrics for the gateway
// to use in routing decisions.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Worker-ID", workerID)

	total := metrics.totalRequests.Load()
	avgMs := 0.0
	if total > 0 {
		avgMs = float64(metrics.totalLatencyNs.Load()) / float64(total) / 1e6
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"worker_id":          workerID,
		"active_connections": metrics.activeConns.Load(),
		"total_requests":     total,
		"avg_response_ms":    math.Round(avgMs*100) / 100,
	})
}

// ── Main ────────────────────────────────────────────────────

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", handlePing)
	mux.HandleFunc("/work", handleWork)
	mux.HandleFunc("/metrics", handleMetrics)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("🔧 Worker %s starting on %s", workerID, addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("❌ Worker %s failed to start: %v", workerID, err)
	}
}
