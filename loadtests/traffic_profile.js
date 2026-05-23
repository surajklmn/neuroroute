// ──────────────────────────────────────────────────────────────
//  NeuroRoute — k6 Load Test: Mixed Traffic Profile
//
//  Generates a realistic 80% light / 20% heavy request pattern
//  across 100 virtual users for 2 minutes.
//
//  Traffic distribution:
//    40% — GET  /ping          (light, instant)
//    40% — POST /work?type=light (light, SHA-256 hashing)
//    10% — POST /work?type=heavy (heavy, prime sieve)
//    10% — POST /work?type=matrix (heavy, matrix multiply)
//
//  Usage:
//    k6 run loadtests/traffic_profile.js
//    k6 run --out csv=loadtests/results/results.csv loadtests/traffic_profile.js
// ──────────────────────────────────────────────────────────────

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

// ── Custom Metrics ──────────────────────────────────────────

const errorRate = new Rate("errors");
const lightLatency = new Trend("light_latency", true);
const heavyLatency = new Trend("heavy_latency", true);

// ── Test Configuration ──────────────────────────────────────

export const options = {
  scenarios: {
    mixed_traffic: {
      executor: "constant-vus",
      vus: 100,
      duration: "2m",
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<2000", "p(99)<5000"],
    errors: ["rate<0.05"],
  },
};

// ── Constants ───────────────────────────────────────────────

const GATEWAY_URL = __ENV.GATEWAY_URL || "http://localhost:8000";

const LIGHT_PAYLOAD = JSON.stringify({
  data: "neuroroute-light-payload-for-sha256-hashing",
  timestamp: Date.now(),
});

const HEAVY_PAYLOAD = JSON.stringify({
  data: "neuroroute-heavy-payload",
});

const HEADERS = {
  "Content-Type": "application/json",
};

// ── Request Generators ──────────────────────────────────────

function pingRequest() {
  const res = http.get(`${GATEWAY_URL}/ping`);

  check(res, {
    "ping: status 200": (r) => r.status === 200,
    "ping: has status field": (r) => {
      try {
        return JSON.parse(r.body).status === "ok";
      } catch {
        return false;
      }
    },
  });

  errorRate.add(res.status !== 200);
  lightLatency.add(res.timings.duration);
}

function lightWorkRequest() {
  const res = http.post(`${GATEWAY_URL}/work?type=light`, LIGHT_PAYLOAD, {
    headers: HEADERS,
  });

  check(res, {
    "light: status 200": (r) => r.status === 200,
    "light: has worker_id": (r) => {
      try {
        return JSON.parse(r.body).worker_id !== undefined;
      } catch {
        return false;
      }
    },
    "light: has hash": (r) => {
      try {
        return JSON.parse(r.body).hash !== undefined;
      } catch {
        return false;
      }
    },
  });

  errorRate.add(res.status !== 200);
  lightLatency.add(res.timings.duration);
}

function heavyWorkRequest() {
  const res = http.post(`${GATEWAY_URL}/work?type=heavy`, HEAVY_PAYLOAD, {
    headers: HEADERS,
  });

  check(res, {
    "heavy: status 200": (r) => r.status === 200,
    "heavy: has prime_count": (r) => {
      try {
        return JSON.parse(r.body).prime_count > 0;
      } catch {
        return false;
      }
    },
  });

  errorRate.add(res.status !== 200);
  heavyLatency.add(res.timings.duration);
}

function matrixWorkRequest() {
  const res = http.post(`${GATEWAY_URL}/work?type=matrix`, HEAVY_PAYLOAD, {
    headers: HEADERS,
  });

  check(res, {
    "matrix: status 200": (r) => r.status === 200,
    "matrix: has trace": (r) => {
      try {
        return JSON.parse(r.body).trace !== undefined;
      } catch {
        return false;
      }
    },
  });

  errorRate.add(res.status !== 200);
  heavyLatency.add(res.timings.duration);
}

// ── Main Test Function ──────────────────────────────────────

export default function () {
  // Weighted random selection: 80% light, 20% heavy
  const roll = Math.random();

  if (roll < 0.4) {
    // 40% — ping (light)
    pingRequest();
  } else if (roll < 0.8) {
    // 40% — light work
    lightWorkRequest();
  } else if (roll < 0.9) {
    // 10% — heavy work (prime sieve)
    heavyWorkRequest();
  } else {
    // 10% — heavy work (matrix)
    matrixWorkRequest();
  }

  // Small think time between requests (50-150ms)
  sleep(0.05 + Math.random() * 0.1);
}

// ── Summary Handler ─────────────────────────────────────────

export function handleSummary(data) {
  const summary = {
    timestamp: new Date().toISOString(),
    total_requests: data.metrics.http_reqs
      ? data.metrics.http_reqs.values.count
      : 0,
    error_rate: data.metrics.errors
      ? data.metrics.errors.values.rate
      : 0,
    http_req_duration: data.metrics.http_req_duration
      ? {
          avg: data.metrics.http_req_duration.values.avg,
          p90: data.metrics.http_req_duration.values["p(90)"],
          p95: data.metrics.http_req_duration.values["p(95)"],
          p99: data.metrics.http_req_duration.values["p(99)"],
          max: data.metrics.http_req_duration.values.max,
        }
      : {},
    light_latency: data.metrics.light_latency
      ? {
          avg: data.metrics.light_latency.values.avg,
          p95: data.metrics.light_latency.values["p(95)"],
          p99: data.metrics.light_latency.values["p(99)"],
        }
      : {},
    heavy_latency: data.metrics.heavy_latency
      ? {
          avg: data.metrics.heavy_latency.values.avg,
          p95: data.metrics.heavy_latency.values["p(95)"],
          p99: data.metrics.heavy_latency.values["p(99)"],
        }
      : {},
  };

  console.log("\n══════════════════════════════════════════════");
  console.log("  NeuroRoute Load Test Summary");
  console.log("══════════════════════════════════════════════");
  console.log(`  Total Requests:  ${summary.total_requests}`);
  console.log(`  Error Rate:      ${(summary.error_rate * 100).toFixed(2)}%`);
  console.log(`  Avg Latency:     ${summary.http_req_duration.avg?.toFixed(2)}ms`);
  console.log(`  p95 Latency:     ${summary.http_req_duration.p95?.toFixed(2)}ms`);
  console.log(`  p99 Latency:     ${summary.http_req_duration.p99?.toFixed(2)}ms`);
  console.log(`  Max Latency:     ${summary.http_req_duration.max?.toFixed(2)}ms`);
  console.log("──────────────────────────────────────────────");
  console.log(`  Light p99:       ${summary.light_latency.p99?.toFixed(2)}ms`);
  console.log(`  Heavy p99:       ${summary.heavy_latency.p99?.toFixed(2)}ms`);
  console.log("══════════════════════════════════════════════\n");

  return {
    "loadtests/results/summary.json": JSON.stringify(summary, null, 2),
    stdout: "", // suppress default k6 summary (we printed our own)
  };
}
