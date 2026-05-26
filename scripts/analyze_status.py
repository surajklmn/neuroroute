import pandas as pd
import numpy as np

for name, path in [("Round-Robin", "loadtests/results/round_robin.csv"), ("Smart", "loadtests/results/smart_route.csv")]:
    df = pd.read_csv(path, low_memory=False)
    reqs = df[df['metric_name'] == 'http_req_duration'].copy()
    reqs['metric_value'] = reqs['metric_value'].astype(float)
    light = reqs[~reqs['url'].fillna('').str.contains('heavy|matrix', case=False)]['metric_value']
    print(f"\n=== {name} — Light Request Distribution ({len(light)} requests) ===")
    for pct in [0, 10, 25, 50, 75, 90, 95, 99, 100]:
        print(f"  p{pct:3d}: {np.percentile(light, pct):>10.2f} ms")
    buckets = [(0,5),(5,20),(20,100),(100,1000),(1000,5000),(5000,999999)]
    print("\n  Latency buckets:")
    for lo, hi in buckets:
        count = ((light >= lo) & (light < hi)).sum()
        pct_of_total = count/len(light)*100
        bar = 'X' * int(pct_of_total/2)
        label = f"<{hi}ms" if hi < 999999 else f">={lo}ms"
        print(f"  [{lo:>5}-{label:>10}]: {count:5d} ({pct_of_total:5.1f}%) {bar}")
