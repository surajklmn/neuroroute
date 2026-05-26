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
    import matplotlib.ticker as ticker
    CHARTING_AVAILABLE = True
except ImportError:
    CHARTING_AVAILABLE = False

# Paths
RR_CSV = "loadtests/results/unsegregated.csv"
SMART_CSV = "loadtests/results/smart_route.csv"
OUTPUT_CHART = "loadtests/results/comparison_chart.png"

def parse_k6_csv(filepath):
    """Load and parse a raw k6 CSV export."""
    if not os.path.exists(filepath):
        print(f"❌ File not found: {filepath}")
        return None
        
    print(f"📖 Parsing raw metrics from {filepath}...")
    
    # Read CSV
    df = pd.read_csv(filepath, low_memory=False)
    
    # Filter for request duration metric only
    df_reqs = df[df['metric_name'] == 'http_req_duration'].copy()
    
    # Ensure types
    df_reqs['metric_value'] = df_reqs['metric_value'].astype(float)
    
    # Categorize requests into 3 classes:
    # Class 0 (Light): ping or type=light
    # Class 1 (Medium): type=heavy
    # Class 2 (Heavy): type=matrix
    def get_class_label(url):
        url_str = str(url).lower()
        if 'ping' in url_str or 'type=light' in url_str:
            return 0
        elif 'type=heavy' in url_str:
            return 1
        elif 'type=matrix' in url_str:
            return 2
        else:
            return 0
            
    df_reqs['class_label'] = df_reqs['url'].fillna('').apply(get_class_label)
    
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
    
    # Light requests (Class 0)
    df_light = df[df['class_label'] == 0]
    stats['light'] = compute_metrics(df_light)
    
    # Medium requests (Class 1)
    df_medium = df[df['class_label'] == 1]
    stats['medium'] = compute_metrics(df_medium)
    
    # Heavy requests (Class 2)
    df_heavy = df[df['class_label'] == 2]
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
        ("Light Traffic (Insulated Fast Lane - Class 0)", "light"),
        ("Medium Traffic (Isolated Medium Lane - Class 1)", "medium"),
        ("Heavy Traffic (Quarantined Slow Lane - Class 2)", "heavy")
    ]
    
    for title, key in groups:
        rr = rr_stats[key]
        sm = smart_stats[key]
        
        # Calculate improvements
        latency_imp = ((rr['mean'] - sm['mean']) / rr['mean'] * 100) if rr['mean'] > 0 else 0
        p95_imp = ((rr['p95'] - sm['p95']) / rr['p95'] * 100) if rr['p95'] > 0 else 0
        error_diff = rr['error_rate'] - sm['error_rate']
        
        print(f"### 📊 {title}")
        print("| Metric | Baseline (Unsegregated) | NeuroRoute (Predictive) | Delta / Change |")
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
    """Plot publication-quality 4-panel comparison charts using matplotlib."""
    if not CHARTING_AVAILABLE:
        print("⚠️  matplotlib is not installed in the local virtual environment. Skipping charting.")
        return
        
    print(f"🎨 Generating visualization chart → {OUTPUT_CHART}")
    
    # ── Prepare data slices ──
    rr_light = rr_df[rr_df['class_label'] == 0]['metric_value']
    sm_light = smart_df[smart_df['class_label'] == 0]['metric_value']
    rr_medium = rr_df[rr_df['class_label'] == 1]['metric_value']
    sm_medium = smart_df[smart_df['class_label'] == 1]['metric_value']
    rr_heavy = rr_df[rr_df['class_label'] == 2]['metric_value']
    sm_heavy = smart_df[smart_df['class_label'] == 2]['metric_value']
    
    # ── Color palette ──
    C_RR = '#f87171'       # Warm coral red
    C_RR_DARK = '#dc2626'  # Darker red for accents
    C_SM = '#4ade80'       # Fresh green
    C_SM_DARK = '#16a34a'  # Darker green for accents
    C_BG = '#0f172a'       # Slate 900 background
    C_CARD = '#1e293b'     # Slate 800 card background
    C_TEXT = '#e2e8f0'     # Slate 200 text
    C_MUTED = '#94a3b8'    # Slate 400 muted text
    C_GRID = '#334155'     # Slate 700 grid lines
    
    # ── Create figure ──
    fig, axes = plt.subplots(2, 2, figsize=(16, 12))
    fig.patch.set_facecolor(C_BG)
    
    for ax in axes.flat:
        ax.set_facecolor(C_CARD)
        ax.tick_params(colors=C_TEXT, labelsize=9)
        ax.spines['top'].set_visible(False)
        ax.spines['right'].set_visible(False)
        ax.spines['bottom'].set_color(C_GRID)
        ax.spines['left'].set_color(C_GRID)
        ax.yaxis.label.set_color(C_TEXT)
        ax.xaxis.label.set_color(C_TEXT)
        ax.title.set_color(C_TEXT)
    
    # ═══════════════════════════════════════════════════════════
    # Panel 1: Light Request Latency Distribution (Box Plot)
    # ═══════════════════════════════════════════════════════════
    ax1 = axes[0, 0]
    
    box_data = [rr_light.values, sm_light.values]
    bp = ax1.boxplot(
        box_data,
        patch_artist=True,
        tick_labels=['Unsegregated\n(Baseline)', 'NeuroRoute\n(ML-Insulated)'],
        widths=0.5,
        showfliers=True,
        flierprops=dict(marker='o', markersize=3, alpha=0.3),
    )
    
    for patch, color in zip(bp['boxes'], [C_RR, C_SM]):
        patch.set_facecolor(color)
        patch.set_alpha(0.75)
        patch.set_edgecolor('white')
        patch.set_linewidth(1.2)
    for median in bp['medians']:
        median.set(color='white', linewidth=2)
    for whisker in bp['whiskers']:
        whisker.set(color=C_MUTED, linewidth=1)
    for cap in bp['caps']:
        cap.set(color=C_MUTED, linewidth=1)
    
    ax1.set_title("Light Request Latency\n(Head-of-Line Blocking Proof)", fontsize=12, fontweight='bold', pad=12)
    ax1.set_ylabel("Latency (ms)", fontsize=10)
    ax1.set_yscale('log')
    ax1.yaxis.set_major_formatter(ticker.ScalarFormatter())
    ax1.grid(axis='y', alpha=0.2, color=C_GRID)
    
    # Add improvement annotation (use p95 — tail latency is what matters)
    rr_p95 = np.percentile(rr_light, 95)
    sm_p95 = np.percentile(sm_light, 95)
    imp = ((rr_p95 - sm_p95) / rr_p95 * 100)
    ax1.annotate(
        f'p95: {imp:+.0f}% lower',
        xy=(2, sm_p95), xytext=(2.35, sm_p95 * 1.5),
        fontsize=10, fontweight='bold', color=C_SM,
        arrowprops=dict(arrowstyle='->', color=C_SM, lw=1.5),
        ha='left',
    )
    
    # ═══════════════════════════════════════════════════════════
    # Panel 2: Latency Breakdown — Avg & p95 for Light + Heavy
    # ═══════════════════════════════════════════════════════════
    ax2 = axes[0, 1]
    
    categories = [
        'Light\nAvg', 'Light\np95',
        'Medium\nAvg', 'Medium\np95',
        'Heavy\nAvg', 'Heavy\np95'
    ]
    rr_vals = [
        np.mean(rr_light) if len(rr_light) > 0 else 0,
        np.percentile(rr_light, 95) if len(rr_light) > 0 else 0,
        np.mean(rr_medium) if len(rr_medium) > 0 else 0,
        np.percentile(rr_medium, 95) if len(rr_medium) > 0 else 0,
        np.mean(rr_heavy) if len(rr_heavy) > 0 else 0,
        np.percentile(rr_heavy, 95) if len(rr_heavy) > 0 else 0,
    ]
    sm_vals = [
        np.mean(sm_light) if len(sm_light) > 0 else 0,
        np.percentile(sm_light, 95) if len(sm_light) > 0 else 0,
        np.mean(sm_medium) if len(sm_medium) > 0 else 0,
        np.percentile(sm_medium, 95) if len(sm_medium) > 0 else 0,
        np.mean(sm_heavy) if len(sm_heavy) > 0 else 0,
        np.percentile(sm_heavy, 95) if len(sm_heavy) > 0 else 0,
    ]
    
    x = np.arange(len(categories))
    width = 0.25
    
    bars_rr = ax2.bar(x - width/2, rr_vals, width, label='Unsegregated (Baseline)', color=C_RR, alpha=0.85, edgecolor='white', linewidth=0.5)
    bars_sm = ax2.bar(x + width/2, sm_vals, width, label='NeuroRoute', color=C_SM, alpha=0.85, edgecolor='white', linewidth=0.5)
    
    # Add value labels on bars
    for bar_group, color in [(bars_rr, C_RR_DARK), (bars_sm, C_SM_DARK)]:
        for bar in bar_group:
            height = bar.get_height()
            if height > 0:
                label = f'{height:.0f}' if height >= 10 else f'{height:.1f}'
                ax2.text(
                    bar.get_x() + bar.get_width() / 2., height * 1.1,
                    label, ha='center', va='bottom', fontsize=7.5,
                    color=C_TEXT, fontweight='bold',
                )
    
    ax2.set_title("Latency Breakdown by Request Type", fontsize=12, fontweight='bold', pad=12)
    ax2.set_ylabel("Latency (ms)", fontsize=10)
    ax2.set_xticks(x)
    ax2.set_xticklabels(categories, fontsize=9)
    ax2.set_yscale('log')
    ax2.yaxis.set_major_formatter(ticker.ScalarFormatter())
    ax2.legend(frameon=True, facecolor=C_CARD, edgecolor=C_GRID, labelcolor=C_TEXT, fontsize=9, loc='upper left')
    ax2.grid(axis='y', alpha=0.2, color=C_GRID)
    
    # ═══════════════════════════════════════════════════════════
    # Panel 3: Error Rate % Comparison (fair — normalised by request count)
    # error_code 1502 = HTTP 502 from backend
    # error_code 1050 = k6 client-side connection timeout (status == 0)
    # ═══════════════════════════════════════════════════════════
    ax3 = axes[1, 0]

    rr_n = len(rr_df)
    sm_n = len(smart_df)

    # Use error_code column for precise categorisation (more reliable than status cast)
    rr_ec = rr_df['error_code'].fillna(0).astype(float)
    sm_ec = smart_df['error_code'].fillna(0).astype(float)

    rr_total_rate   = (rr_df['is_error'].sum()    / rr_n) * 100
    sm_total_rate   = (smart_df['is_error'].sum() / sm_n) * 100
    rr_502_rate     = ((rr_ec == 1502).sum()       / rr_n) * 100
    sm_502_rate     = ((sm_ec == 1502).sum()       / sm_n) * 100
    rr_timeout_rate = ((rr_ec == 1050).sum()       / rr_n) * 100
    sm_timeout_rate = ((sm_ec == 1050).sum()       / sm_n) * 100

    err_categories = ['Total Error\nRate', '502 Rate\n(Backend)', 'Client Timeout\nRate (k6 cutoff)']
    rr_err_vals = [rr_total_rate, rr_502_rate, rr_timeout_rate]
    sm_err_vals = [sm_total_rate, sm_502_rate, sm_timeout_rate]

    x3 = np.arange(len(err_categories))

    bars_rr3 = ax3.bar(x3 - width/2, rr_err_vals, width, label='Unsegregated (Baseline)', color=C_RR, alpha=0.85, edgecolor='white', linewidth=0.5)
    bars_sm3 = ax3.bar(x3 + width/2, sm_err_vals, width, label='NeuroRoute', color=C_SM, alpha=0.85, edgecolor='white', linewidth=0.5)

    # Value labels
    for bar_group in [bars_rr3, bars_sm3]:
        for bar in bar_group:
            height = bar.get_height()
            label = f'{height:.2f}%'
            ypos  = height + 0.05
            ax3.text(bar.get_x() + bar.get_width() / 2., ypos, label,
                     ha='center', va='bottom', fontsize=8.5, color=C_TEXT, fontweight='bold')

    # Highlight zero client-timeout achievement
    if sm_timeout_rate == 0:
        ax3.annotate(
            '0% — Eliminated! ✓',
            xy=(2 + width/2, 0.02), xytext=(2 + width/2, rr_timeout_rate * 0.6),
            fontsize=9, fontweight='bold', color=C_SM,
            arrowprops=dict(arrowstyle='->', color=C_SM, lw=1.5),
            ha='center',
        )

    ax3.set_title("Error Rates — Normalised by Request Count\n"
                  "(1502 = backend 502  |  1050 = k6 client timeout)",
                  fontsize=11, fontweight='bold', pad=12)
    ax3.set_ylabel("Error Rate (%)", fontsize=10)
    ax3.set_xticks(x3)
    ax3.set_xticklabels(err_categories, fontsize=8.5)
    ax3.legend(frameon=True, facecolor=C_CARD, edgecolor=C_GRID, labelcolor=C_TEXT, fontsize=9)
    ax3.grid(axis='y', alpha=0.2, color=C_GRID)
    
    # ═══════════════════════════════════════════════════════════
    # Panel 4: Overall Summary — Key Wins
    # ═══════════════════════════════════════════════════════════
    ax4 = axes[1, 1]
    
    # Calculate key metrics
    rr_light_avg = np.mean(rr_light)
    sm_light_avg = np.mean(sm_light)
    light_improvement = ((rr_light_avg - sm_light_avg) / rr_light_avg * 100)
    
    rr_error_rate = (rr_df['is_error'].sum() / len(rr_df)) * 100
    sm_error_rate = (smart_df['is_error'].sum() / len(smart_df)) * 100
    error_reduction = ((rr_error_rate - sm_error_rate) / rr_error_rate * 100) if rr_error_rate > 0 else 0
    
    rr_overall_avg = np.mean(rr_df['metric_value'])
    sm_overall_avg = np.mean(smart_df['metric_value'])
    overall_improvement = ((rr_overall_avg - sm_overall_avg) / rr_overall_avg * 100)
    
    metrics = [
        ('Light Latency\nReduction', light_improvement),
        ('Error Rate\nReduction', error_reduction),
        ('Overall Avg\nImprovement', overall_improvement),
    ]
    
    x4 = np.arange(len(metrics))
    values = [m[1] for m in metrics]
    labels = [m[0] for m in metrics]
    colors_bar = [C_SM if v > 0 else C_RR for v in values]
    
    bars4 = ax4.bar(x4, values, 0.55, color=colors_bar, alpha=0.85, edgecolor='white', linewidth=0.5)
    
    for bar, val in zip(bars4, values):
        ax4.text(
            bar.get_x() + bar.get_width() / 2., bar.get_height() + 1,
            f'{val:+.1f}%', ha='center', va='bottom', fontsize=12,
            color=C_SM if val > 0 else C_RR, fontweight='bold',
        )
    
    ax4.set_title("NeuroRoute Key Wins (% Improvement)", fontsize=12, fontweight='bold', pad=12)
    ax4.set_ylabel("Improvement %", fontsize=10)
    ax4.set_xticks(x4)
    ax4.set_xticklabels(labels, fontsize=9)
    ax4.axhline(y=0, color=C_MUTED, linewidth=0.8, linestyle='--')
    ax4.grid(axis='y', alpha=0.2, color=C_GRID)
    
    # ── Final layout ──
    fig.suptitle(
        "NeuroRoute — AI-Driven Predictive Load Balancer Benchmarks",
        fontsize=18, fontweight='bold', color=C_TEXT, y=0.98,
    )
    fig.text(
        0.5, 0.01,
        "100 VUs × 2 min   |   80% Light / 20% Heavy Traffic Mix   |   Positive % = NeuroRoute wins",
        ha='center', fontsize=10, color=C_MUTED, style='italic',
    )
    
    plt.tight_layout(rect=[0, 0.03, 1, 0.95])
    
    # Create parent dirs if necessary
    os.makedirs(os.path.dirname(OUTPUT_CHART), exist_ok=True)
    plt.savefig(OUTPUT_CHART, dpi=300, bbox_inches='tight', facecolor=C_BG)
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
