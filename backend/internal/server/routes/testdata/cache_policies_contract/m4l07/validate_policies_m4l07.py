#!/usr/bin/env python3
"""cache_policies_contract/m4l07 自检（F）。只读、轻量，按 RULES §8 直接运行，只用标准库。
用法：python3 -I -B validate_policies_m4l07.py <cache_policies_contract 目录>
- 父目录（v1.7.4.6）与 m3m4（v1.2）的文件逐个与 m4l07/FROZEN.sha256 相等；
- 复用 m3m4 的自检规则，把 routing 的字段集与默认值换成 T2.5 的，并加入两条特殊方向规则、guard_tripped、share_group_no_effect 与静态上限截断的检查；
- 派生样例可由 m3m4 源样例重新生成；invalid/ 全部被拒。"""
import copy, hashlib, importlib.util, json, os, sys
sys.dont_write_bytecode = True
root = sys.argv[1]; md = os.path.join(root, "m4l07"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, p = line.split(None, 1); p = p.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(root, p), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", p); bad += 1
spec = importlib.util.spec_from_file_location("m3m4v", os.path.join(root, "m3m4", "validate_policies_m3m4.py"))
V = importlib.util.module_from_spec(spec); spec.loader.exec_module(V)
K = "keep_sticky_on_busy."
NEW = [({"field": K + "max_consecutive_busy", "type": "int", "min": 1, "max": 5, "cap_source": "gateway.cache_routing.l07_max_consecutive_cap"}, 2),
       ({"field": K + "max_hold_seconds", "type": "int", "min": 10, "max": 300, "cap_source": "gateway.cache_routing.l07_max_hold_seconds_cap"}, 60),
       ({"field": K + "owner_busy_skip_per_min_cap", "type": "int", "min": 0, "max": 1000}, 0),
       ({"field": K + "group_keep_ratio_pct", "type": "int", "min": 1, "max": 20, "unit": "percent", "cap_source": "gateway.cache_routing.l07_group_keep_ratio_cap_pct"}, 5),
       ({"field": K + "health_lookback_seconds", "type": "int", "min": 30, "max": 600}, 120)]
r = {}
for k, v in V.DEFAULTS["routing"].items():
    r[k] = v
    if k == K + "rollout_percent":
        for f, d in NEW: r[f["field"]] = d
V.DEFAULTS["routing"] = r
V.BLOCKERS.add("guard_tripped"); V.BLOCKER_ORDER.append("guard_tripped")
V.WARNINGS.add("share_group_no_effect"); V.WARN_ORDER.insert(V.WARN_ORDER.index("account_options_unavailable"), "share_group_no_effect")
_orig_dir = V.direction
def direction(key, diff):
    """两条特殊规则：health_lookback_seconds 调高为关闭方向；owner_busy_skip_per_min_cap 从非 0 改为 0（自动）为开启方向。"""
    up = down = False; normal = []
    for x in diff:
        f, b, a = x["field"], x["before"], x["after"]
        if f == K + "health_lookback_seconds": up, down = up or a < b, down or a >= b
        elif f == K + "owner_busy_skip_per_min_cap":
            if a == 0 and b != 0: up = True
            elif b == 0 and a != 0: down = True
            else: up, down = up or a > b, down or a < b
        else: normal.append(x)
    if normal:
        lab = _orig_dir(key, normal)
        up, down = up or lab in ("enable", "mixed"), down or lab in ("disable", "mixed")
    return "mixed" if up and down else ("enable" if up else "disable")
V.direction = direction
_orig_cap = V.cap_problems
def cap_problems(p, caps, where):
    res = _orig_cap(p, caps, where)
    if "guard_tripped" in p["effective"].get("blockers", []):   # 护栏暂停也是 blocked（m4l07 README §1）
        res = [x for x in res if "只有在未实现的功能被激活时才为 blocked" not in x]
    return res
V.cap_problems = cap_problems
def transform(o):
    o = copy.deepcopy(o)
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "routing" and x.get("available") and isinstance(x.get("schema"), list):
                i = [f["field"] for f in x["schema"]].index(K + "rollout_percent") + 1
                x["schema"] = x["schema"][:i] + [copy.deepcopy(f) for f, _ in NEW] + x["schema"][i:]
                for d in (x["defaults"]["keep_sticky_on_busy"], x["stored"]["spec"]["keep_sticky_on_busy"]):
                    for f, v in NEW: d.setdefault(f["field"][len(K):], v)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return o
def routings(o):
    found = []
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "routing" and x.get("available") and isinstance(x.get("schema"), list): found.append(x)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return found
def check(o):
    out = V.check(o)
    src = o.get("_derived_from")
    if src:
        want = transform(json.load(open(os.path.join(root, "m3m4", src), encoding="utf-8")))
        if any(want[k] != o[k] for k in ("request", "status", "response")): out.append(f"与 m3m4 源样例 {src} 按规则派生的结果不一致")
    for p in routings(o):
        spec = V.flat(p["stored"]["spec"])
        clipped = set(p["effective"].get("clipped_fields", []))
        for f in p["schema"]:
            if "cap_source" in f and f["field"].startswith(K):
                if (spec.get(f["field"], 0) > f["max"]) != (f["field"] in clipped): out.append(f"{f['field']}: 存储值超过 schema 的 max（当前静态上限）时当且仅当列入 clipped_fields")
        for f, _ in NEW:
            got = next((x for x in p["schema"] if x["field"] == f["field"]), None)
            if got is None or {k: v for k, v in got.items() if k != "max"} != {k: v for k, v in f.items() if k != "max"} or got["max"] > f["max"]:
                out.append(f"{f['field']}: schema 定义不对（max 可以因静态上限调低而更小）")
    return out
scope = json.load(open(os.path.join(md, "SCOPE.json"), encoding="utf-8"))
samples = sorted(f for f in os.listdir(md) if f.endswith(".json") and f != "SCOPE.json")
for f in samples:
    for p in check(json.load(open(os.path.join(md, f), encoding="utf-8"))): print("PROBLEM", f, p); bad += 1
for f in scope["derived"]:
    if "m4l07_" + f not in samples: print("MISSING_DERIVED", f); bad += 1
inv = sorted(os.listdir(os.path.join(md, "invalid")))
for f in inv:
    got = check(json.load(open(os.path.join(md, "invalid", f), encoding="utf-8")))
    print(f"invalid {f}: {'REJECTED — ' + got[0] if got else 'ACCEPTED'}")
    if not got: bad += 1
print("frozen", nf, "samples", len(samples), "derived_sources", len(scope["derived"]), "unchanged_sources", len(scope["unchanged"]), "invalid", len(inv), "problems", bad)
sys.exit(1 if bad else 0)
