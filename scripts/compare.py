"""
──────────────────────────────────────────────────────────────
 NeuroRoute — Benchmark Comparison Script

 Parses raw k6 timeseries CSV files from both Round-Robin and
 Smart-Routing test runs, computes comprehensive metrics,
 prints a beautifully formatted terminal summary, and exports
 a comparative visualization chart.

 Usage:
   ml/.venv/bin/python scripts/compare.py
──────────────────────────────────────────────────────────────
"""

import os
import sys
import pandas as pd
import numpy as np

# Try to import matplotlib for charting, fail gracefully if not available
try:
    import matplotlib.pyplot as plt
    CHARTING_AVAILABLE = True
except ImportError:
    CHARTING_AVAILABLE = False

# Paths
RR_CSV = "loadtests/results/round_robin.csv"
SMART_CSV = "loadtests/results/smart_route.csv"
OUTPUT_CHART = "loadtests/results/comparison_chart.png"

def parse_k6_csv(filepath):
    """Load and parse a raw k6 CSV export."""
    if not os.path.exists(filepath):
        print(f"❌ File not found: {filepath}")
        return None
        
    print(f"📖 Parsing raw metrics from {filepath}...")
    
    # Read CSV
    df = pd.read_csv(filepath)
    
    # Filter for request duration metric only
    df_reqs = df[df['metric_name'] == 'http_req_duration'].copy()
    
    # Ensure types
    df_reqs['metric_value'] = df_reqs['metric_value'].astype(float)
    
    # Categorize requests into Light vs Heavy
    # Light: GET /ping or POST /work?type=light
    # Heavy: POST /work?type=heavy or POST /work?type=matrix
    df_reqs['is_heavy'] = df_reqs['url'].fillna('').apply(
        lambda x: any(term in str(x).lower() for term in ['heavy', 'matrix'])
    )
    
    # Track errors (where status is not 200)
    df_reqs['is_error'] = df_reqs['status'].fillna(200).apply(lambda x: int(x) != 200)
    
    return df_reqs

def calculate_stats(df):
    """Calculate statistical summaries for a test run."""
    if df is None or len(df) == 0:
        return None
        
    stats = {}
    
    # Overall
    stats['overall'] = compute_metrics(df)
    
    # Light requests
    df_light = df[~df['is_heavy']]
    stats['light'] = compute_metrics(df_light)
    
    # Heavy requests
    df_heavy = df[df['is_heavy']]
    stats['heavy'] = compute_metrics(df_heavy)
    
    return stats

def compute_metrics(df):
    if len(df) == 0:
        return {"count": 0, "error_rate": 0.0, "mean": 0.0, "p50": 0.0, "p95": 0.0, "p99": 0.0, "max": 0.0}
        
    vals = df['metric_value'].values
    errors = df['is_error'].values
    
    return {
        "count": len(df),
        "error_rate": (np.sum(errors) / len(df)) * 100,
        "mean": np.mean(vals),
        "p50": np.percentile(vals, 50),
        "p95": np.percentile(vals, 95),
        "p99": np.percentile(vals, 99),
        "max": np.max(vals)
    }

def print_comparison_table(rr_stats, smart_stats):
    """Print an elegant, copy-pasteable markdown table for reports."""
    print("\n" + "═" * 80)
    print(" 🧠 NEUROROUTE BENCHMARK RESULTS COMPARISON")
    print("═" * 80 + "\n")
    
    groups = [
        ("Overall System Traffic", "overall"),
        ("Light Traffic (Insulated Fast Lane)", "light"),
        ("Heavy Traffic (Quarantined Slow Lane)", "heavy")
    ]
    
    for title, key in groups:
        rr = rr_stats[key]
        sm = smart_stats[key]
        
        # Calculate improvements
        latency_imp = ((rr['mean'] - sm['mean']) / rr['mean'] * 100) if rr['mean'] > 0 else 0
        p95_imp = ((rr['p95'] - sm['p95']) / rr['p95'] * 100) if rr['p95'] > 0 else 0
        error_diff = rr['error_rate'] - sm['error_rate']
        
        print(f"### 📊 {title}")
        print("| Metric | Baseline (Round-Robin) | NeuroRoute (Predictive) | Delta / Change |")
        print("| :--- | :---: | :---: | :---: |")
        print(f"| **Request Count** | {rr['count']} | {sm['count']} | {sm['count'] - rr['count']:+d} |")
        print(f"| **Error Rate** | {rr['error_rate']:.2f}% | {sm['error_rate']:.2f}% | {error_diff:+.2f}% |")
        print(f"| **Avg Latency** | {rr['mean']:.1f} ms | {sm['mean']:.1f} ms | {latency_imp:+.1f}% |")
        print(f"| **p50 (Median)** | {rr['p50']:.1f} ms | {sm['p50']:.1f} ms | {((rr['p50'] - sm['p50'])/rr['p50']*100 if rr['p50'] > 0 else 0):+.1f}% |")
        print(f"| **p95 Latency** | {rr['p95']:.1f} ms | {sm['p95']:.1f} ms | {p95_imp:+.1f}% |")
        print(f"| **p99 Latency** | {rr['p99']:.1f} ms | {sm['p99']:.1f} ms | {((rr['p99'] - sm['p99'])/rr['p99']*100 if rr['p99'] > 0 else 0):+.1f}% |")
        print(f"| **Max Latency** | {rr['max']:.1f} ms | {sm['max']:.1f} ms | {((rr['max'] - sm['max'])/rr['max']*100 if rr['max'] > 0 else 0):+.1f}% |")
        print("\n" + "─" * 80 + "\n")

def generate_visualizations(rr_df, smart_df):
    """Plot publication-quality comparison charts using matplotlib."""
    if not CHARTING_AVAILABLE:
        print("⚠️  matplotlib is not installed in the local virtual environment. Skipping charting.")
        return
        
    print(f"🎨 Generating visualization chart → {OUTPUT_CHART}")
    
    # Filter out light requests for both runs
    rr_light = rr_df[~rr_df['is_heavy']]['metric_value']
    sm_light = smart_df[~smart_df['is_heavy']]['metric_value']
    
    # Create figure
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6), sharey=False)
    
    # Style configuration
    plt.style.use('seaborn-v0_8-whitegrid' if 'seaborn-v0_8-whitegrid' in plt.style.available else 'default')
    
    # ── Chart 1: Light Request Latency Distribution (Boxplot) ──
    box_data = [rr_light, sm_light]
    bp = ax1.boxplot(box_data, patch_artist=True, labels=['Round-Robin\n(Unmanaged)', 'NeuroRoute\n(ML-Insulated)'])
    
    # Color settings
    colors = ['#f87171', '#4ade80'] # Sleek red and green
    for patch, color in zip(bp['boxes'], colors):
        patch.set_facecolor(color)
        patch.set_alpha(0.7)
    for median in bp['medians']:
        median.set(color='#1e293b', linewidth=2)
        
    ax1.set_title("Light request Latency (Head-of-Line Blocking Proof)", fontsize=12, fontweight='bold', pad=15)
    ax1.set_ylabel("Latency (milliseconds)", fontsize=10)
    ax1.set_yscale('log') # Logarithmic scale since RR is massive compared to Smart
    
    # ── Chart 2: p95 Latency comparison (Bar Chart) ──
    labels = ['Overall Avg', 'Light p95', 'Heavy p95']
    
    rr_bar = [
        np.mean(rr_df['metric_value']),
        np.percentile(rr_light, 95) if len(rr_light) > 0 else 0,
        np.percentile(rr_df[rr_df['is_heavy']]['metric_value'], 95) if len(rr_df[rr_df['is_heavy']]) > 0 else 0
    ]
    
    sm_bar = [
        np.mean(smart_df['metric_value']),
        np.percentile(sm_light, 95) if len(sm_light) > 0 else 0,
        np.percentile(smart_df[smart_df['is_heavy']]['metric_value'], 95) if len(smart_df[smart_df['is_heavy']]) > 0 else 0
    ]
    
    x = np.arange(len(labels))
    width = 0.35
    
    ax2.bar(x - width/2, rr_bar, width, label='Round-Robin', color='#f87171', alpha=0.8)
    ax2.bar(x + width/2, sm_bar, width, label='NeuroRoute', color='#4ade80', alpha=0.8)
    
    ax2.set_title("p95 Latency Breakdown per Pool", fontsize=12, fontweight='bold', pad=15)
    ax2.set_ylabel("Latency (milliseconds)", fontsize=10)
    ax2.set_xticks(x)
    ax2.set_xticklabels(labels)
    ax2.set_yscale('log')
    ax2.legend(frameon=True, facecolor='white', edgecolor='none')
    
    plt.suptitle("NeuroRoute AI-Driven Predictive Load Balancer Benchmarks", fontsize=16, fontweight='bold', y=0.98)
    plt.tight_layout()
    
    # Create parent dirs if necessary
    os.makedirs(os.path.dirname(OUTPUT_CHART), exist_ok=True)
    plt.savefig(OUTPUT_CHART, dpi=300, bbox_inches='tight')
    print(f"✅ Success! Comparative chart saved to: {OUTPUT_CHART}")

def main():
    print("🚀 Running NeuroRoute comparative analysis...")
    
    rr_df = parse_k6_csv(RR_CSV)
    smart_df = parse_k6_csv(SMART_CSV)
    
    if rr_df is None or smart_df is None:
        print("\n❌ Error: Missing benchmark data. Make sure you run both tests:")
        print("   1. `make test-rr`     (wait 2 mins)")
        print("   2. `make test-smart`  (wait 2 mins)")
        sys.exit(1)
        
    rr_stats = calculate_stats(rr_df)
    smart_stats = calculate_stats(smart_df)
    
    print_comparison_table(rr_stats, smart_stats)
    generate_visualizations(rr_df, smart_df)

if __name__ == '__main__':
    main()
