#!/usr/bin/env python3
"""Quick analyzer for test results JSON files."""
import json, sys
from collections import defaultdict

def analyze(path):
    with open(path) as f:
        data = json.load(f)
    
    results = data["results"]
    print(f"File: {path}")
    print(f"Timestamp: {data['timestamp']}")
    print(f"Base URL: {data['base_url']}")
    print(f"Concurrency: {data['concurrency']}")
    print(f"Total results: {len(results)}")
    print()

    by_label = defaultdict(list)
    for r in results:
        by_label[r["test_label"]].append(r)

    for label in sorted(by_label.keys()):
        rr = by_label[label]
        ok = [r for r in rr if r.get("status") in (200, 206) and r.get("error") is None]
        fail = [r for r in rr if r not in ok]
        ttfbs = sorted([r["ttfb_s"] * 1000 for r in ok if r.get("ttfb_s")])
        rates = sorted([r["throughput_mbps"] for r in ok if r.get("throughput_mbps")])
        elapsed = sorted([r["elapsed_s"] * 1000 for r in ok])

        print(f"[{label}] {len(ok)} OK / {len(fail)} FAIL")
        if fail:
            errors = set(str(r.get("error", "?"))[:80] for r in fail)
            statuses = set(r.get("status") for r in fail)
            print(f"  Statuses: {statuses}")
            for e in errors:
                print(f"  Error: {e}")
        if ttfbs:
            p50 = ttfbs[len(ttfbs) // 2]
            p95 = ttfbs[int(len(ttfbs) * 0.95)]
            print(f"  TTFB  -> min={min(ttfbs):.0f}ms  avg={sum(ttfbs)/len(ttfbs):.0f}ms  max={max(ttfbs):.0f}ms  p50={p50:.0f}ms  p95={p95:.0f}ms")
        if rates:
            p50 = rates[len(rates) // 2]
            print(f"  Rate  -> min={min(rates):.2f}  avg={sum(rates)/len(rates):.2f}  max={max(rates):.2f}  p50={p50:.2f} MB/s")
        if elapsed:
            print(f"  Total -> min={min(elapsed):.0f}ms  avg={sum(elapsed)/len(elapsed):.0f}ms  max={max(elapsed):.0f}ms")
        print()

    # Ramp-up analysis
    ramp_labels = sorted([l for l in by_label if l.startswith("ramp_c")], key=lambda x: int(x.split("c")[1]))
    if ramp_labels:
        print("=== RAMP-UP SCALING ===")
        for label in ramp_labels:
            rr = by_label[label]
            ok = [r for r in rr if r.get("status") in (200, 206) and r.get("error") is None]
            if not ok:
                print(f"  {label}: ALL FAILED")
                continue
            ttfbs = [r["ttfb_s"] * 1000 for r in ok if r.get("ttfb_s")]
            rates = [r["throughput_mbps"] for r in ok]
            avg_ttfb = sum(ttfbs) / len(ttfbs) if ttfbs else 0
            avg_rate = sum(rates) / len(rates) if rates else 0
            agg_rate = sum(rates)
            print(f"  {label:>10s}: ok={len(ok):>2d}  avg_ttfb={avg_ttfb:>7.0f}ms  avg_rate={avg_rate:.2f} MB/s  agg_rate={agg_rate:.2f} MB/s")
        print()

    # Overall
    all_ok = [r for r in results if r.get("status") in (200, 206) and r.get("error") is None]
    all_fail = len(results) - len(all_ok)
    print(f"=== OVERALL: {len(all_ok)} OK / {all_fail} FAIL / {len(results)} total ===")
    if all_ok:
        all_ttfbs = sorted([r["ttfb_s"] * 1000 for r in all_ok if r.get("ttfb_s")])
        all_rates = sorted([r["throughput_mbps"] for r in all_ok])
        if all_ttfbs:
            p95 = all_ttfbs[int(len(all_ttfbs) * 0.95)]
            print(f"  TTFB  -> min={min(all_ttfbs):.0f}ms  avg={sum(all_ttfbs)/len(all_ttfbs):.0f}ms  max={max(all_ttfbs):.0f}ms  p95={p95:.0f}ms")
        if all_rates:
            p50 = all_rates[len(all_rates) // 2]
            print(f"  Rate  -> min={min(all_rates):.2f}  avg={sum(all_rates)/len(all_rates):.2f}  max={max(all_rates):.2f}  p50={p50:.2f} MB/s")

if __name__ == "__main__":
    for path in sys.argv[1:]:
        analyze(path)
        print()
