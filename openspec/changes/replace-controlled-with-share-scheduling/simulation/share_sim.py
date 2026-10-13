#!/usr/bin/env python3
"""份额调度规划的参考仿真（仅标准库、固定随机种子、无网络、不读写仓库外文件）。

运行：python3 -I openspec/changes/replace-controlled-with-share-scheduling/simulation/share_sim.py

本脚本只验证算法性质，不是网关实现验收：
  1. Sub2API 实验调度器（OpenAI advanced scheduler）默认权重下，优先级只是软因子。
  2. 加权会话一致性哈希（Weighted Rendezvous / HRW，对数法）的比例性与最小扰动。
  3. 在会话亲和前提下，按请求数/轻尾成本/重尾成本统计的份额偏差，以及有界纠偏的作用。
"""
import hashlib
import math
import random
import struct
from collections import Counter

# ---------------------------------------------------------------------------
# 1. Sub2API advanced scheduler: score = Σ weight*factor, Top-K, then weighted
#    random with weight = score - min + 1 (openai_account_scheduler.go).
#    Defaults from backend/internal/config/config.go: priority 1.0, load 1.0,
#    queue 0.7, error_rate 0.8, ttft 0.5 (reset/quota/cost 0).
# ---------------------------------------------------------------------------
W = dict(priority=1.0, load=1.0, queue=0.7, error=0.8, ttft=0.5)


def adv_scores(prios, loads=None, errs=None):
    loads = loads or [0] * len(prios)
    errs = errs or [0.0] * len(prios)
    lo, hi = min(prios), max(prios)
    out = []
    for p, load, err in zip(prios, loads, errs):
        pf = 1.0 if hi == lo else 1 - (p - lo) / (hi - lo)
        out.append(W["priority"] * pf + W["load"] * (1 - min(load, 100) / 100)
                   + W["queue"] * 1 + W["error"] * (1 - err) + W["ttft"] * 0.5)
    return out


def first_pick_share(scores):
    low = min(scores)
    weights = [s - low + 1 for s in scores]
    total = sum(weights)
    return [w / total for w in weights]


# ---------------------------------------------------------------------------
# 2. Weighted rendezvous hashing, logarithm method: pick argmin -ln(u)/w.
# ---------------------------------------------------------------------------
def u01(key, account):
    raw = hashlib.blake2b(f"{key}|{account}".encode(), digest_size=8).digest()
    return (struct.unpack(">Q", raw)[0] + 1) / 2 ** 64  # (0, 1]


def hrw_pick(key, weights):
    return min(weights, key=lambda a: -math.log(u01(key, a)) / weights[a])


# ---------------------------------------------------------------------------
# 3. Session-level placement vs realized share (minute steps, 8 hours).
#    unit=requests: every active session sends 1 request/min.
#    unit=lognormal1: per-session cost rate ~ LogNormal(0, 1) (moderate tail).
#    unit=pareto1.2: per-session cost rate ~ Pareto(1.2) (infinite variance).
#    mode=hrw: sticky sessions placed by HRW with configured weights.
#    mode=corrected: HRW with bounded deficit correction (gamma=1, clamp 0.1..10).
#    mode=per_request: no affinity, every request re-placed (cache-hostile).
# ---------------------------------------------------------------------------
def run(dist, mode, seed, minutes=480, gamma=1.0, cmin=0.1, cmax=10.0, tau=60.0):
    rnd = random.Random(seed)
    weights = {"A": 5, "B": 3, "C": 2}
    total_w = sum(weights.values())
    target = {a: weights[a] / total_w for a in weights}
    ewma = {a: 0.0 for a in weights}
    active, hourly = [], []
    for minute in range(minutes):
        for a in ewma:
            ewma[a] *= math.exp(-1 / tau)
        effective = dict(weights)
        volume = sum(ewma.values())
        if mode == "corrected" and volume > 1e-9:
            for a in weights:
                realized = max(ewma[a] / volume, 1e-6)
                effective[a] = weights[a] * min(cmax, max(cmin, (target[a] / realized) ** gamma))
        arrivals = sum(1 for _ in range(40) if rnd.random() < 0.5)
        for i in range(arrivals):
            duration = max(1, int(rnd.expovariate(1 / 20)))
            if dist == "pareto1.2":
                rate = rnd.paretovariate(1.2)
            elif dist == "lognormal1":
                rate = rnd.lognormvariate(0, 1.0)
            else:
                rate = 1.0
            account = None if mode == "per_request" else hrw_pick(f"{seed}-{minute}-{i}", effective)
            active.append([account, duration, rate, f"{seed}-{minute}-{i}"])
        survivors = []
        for item in active:
            account, remaining, rate, key = item
            if mode == "per_request":
                account = hrw_pick(f"r-{key}-{remaining}", weights)
            ewma[account] += rate
            if remaining > 1:
                item[1] = remaining - 1
                survivors.append(item)
        active = survivors
        if minute % 60 == 59 and minute >= 120:
            volume = sum(ewma.values())
            hourly.append(max(abs(ewma[a] / volume - target[a]) for a in weights))
    return sum(hourly) / len(hourly), max(hourly)


def main():
    print("== 1. Sub2API advanced scheduler, idle accounts, no runtime samples ==")
    for prios in ([1, 2], [1, 1, 2], [1, 5], [1, 2, 3]):
        scores = adv_scores(prios)
        share = first_pick_share(scores)
        print(f"  priorities={prios} scores={[round(s, 2) for s in scores]} "
              f"first-pick share={[f'{x:.1%}' for x in share]}")
    share = first_pick_share(adv_scores([1, 2], loads=[50, 0]))
    print(f"  priorities=[1, 2], primary at 50% load -> first-pick share={[f'{x:.1%}' for x in share]}")

    n = 200_000
    keys = [f"sess-{i}" for i in range(n)]
    base = {"A": 5, "B": 3, "C": 2}
    pick = {k: hrw_pick(k, base) for k in keys}
    count = Counter(pick.values())
    print("\n== 2a. HRW proportionality, weights A:B:C = 5:3:2, 200k session keys ==")
    print("  shares:", {a: f"{count[a] / n:.2%}" for a in sorted(count)}, "target: A 50% B 30% C 20%")

    without_b = {"A": 5, "C": 2}
    moved = sum(1 for k in keys if hrw_pick(k, without_b) != pick[k])
    moved_others = sum(1 for k in keys if pick[k] != "B" and hrw_pick(k, without_b) != pick[k])
    rehomed = Counter(hrw_pick(k, without_b) for k in keys if pick[k] == "B")
    print("\n== 2b. Minimal disruption when B becomes ineligible ==")
    print(f"  keys moved: {moved / n:.2%} (B owned {count['B'] / n:.2%}); keys moved that were not on B: {moved_others}")
    print("  B keys re-homed to:", {a: f"{v / sum(rehomed.values()):.1%}" for a, v in sorted(rehomed.items())},
          "(expected A 71.4% / C 28.6%)")

    changed_weights = {"A": 5, "B": 3, "C": 4}
    changed = sum(1 for k in keys if hrw_pick(k, changed_weights) != pick[k])
    count2 = Counter(hrw_pick(k, changed_weights) for k in keys)
    print("\n== 2c. Weight change C 2 -> 4 (affects placement of NEW sessions only) ==")
    print("  new shares:", {a: f"{count2[a] / n:.2%}" for a in sorted(count2)}, "target A 41.7% B 25.0% C 33.3%")
    print(f"  keys whose first choice changes: {changed / n:.2%} (optimal minimum 13.33%)")

    print("\n== 3. Realized share vs target (A:B:C = 50/30/20), metric = max per-account |share - target|")
    print("     over rolling 1h windows; mean over hours / worst over 20 seeds ==")
    print("  Sub2API LRU at low load ignores weights (~equal split): structural deviation = 16.7%")
    for dist in ("requests", "lognormal1", "pareto1.2"):
        for mode in ("hrw", "corrected", "per_request"):
            results = [run(dist, mode, seed) for seed in range(1, 21)]
            mean_dev = sum(r[0] for r in results) / len(results)
            worst = max(r[1] for r in results)
            print(f"  unit={dist:10s} mode={mode:11s}: mean {mean_dev:5.1%}  worst {worst:5.1%}")


if __name__ == "__main__":
    main()
