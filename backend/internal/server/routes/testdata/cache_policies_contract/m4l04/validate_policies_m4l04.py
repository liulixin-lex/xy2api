#!/usr/bin/env python3
"""cache_policies_contract/m4l04 自检（F）。只读、轻量，按 RULES §8 直接运行，只用标准库。
用法：python3 -I validate_policies_m4l04.py <cache_policies_contract 目录>
- 父目录（v1.7.4.6）与 m3m4（v1.2）的文件逐个与 m4l04/FROZEN.sha256 相等；
- 复用 m3m4 的自检规则，把 anthropic_identity 的字段集与默认值换成 T2.2 的，并加入错误码 exceeds_rollout_percent；
- 派生样例可由 m3m4 源样例重新生成；存储值中 metadata_rollout_percent ≤ rollout_percent；invalid/ 全部被拒。"""
import copy, hashlib, importlib.util, json, os, sys
root = sys.argv[1]; md = os.path.join(root, "m4l04"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, p = line.split(None, 1); p = p.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(root, p), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", p); bad += 1
spec = importlib.util.spec_from_file_location("m3m4v", os.path.join(root, "m3m4", "validate_policies_m3m4.py"))
V = importlib.util.module_from_spec(spec); spec.loader.exec_module(V)
V.DEFAULTS["anthropic_identity"].update(metadata_rollout_percent=0, max_inflight_per_anchor=2)
V.CODES.add("exceeds_rollout_percent")
F_MRP = {"field": "metadata_rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"}
F_MIA = {"field": "max_inflight_per_anchor", "type": "int", "min": 1, "max": 16}
def transform(o):
    o = copy.deepcopy(o)
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "anthropic_identity" and x.get("available") and isinstance(x.get("schema"), list):
                x["schema"] = x["schema"] + [copy.deepcopy(F_MRP), copy.deepcopy(F_MIA)]
                x["defaults"].update(metadata_rollout_percent=0, max_inflight_per_anchor=2)
                x["stored"]["spec"].setdefault("metadata_rollout_percent", 0)
                x["stored"]["spec"].setdefault("max_inflight_per_anchor", 2)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return o
def ais(o):
    found = []
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "anthropic_identity" and x.get("available") and isinstance(x.get("schema"), list): found.append(x)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return found
def check(o):
    out = V.check(o)
    src = o.get("_derived_from")
    if src:
        sp = os.path.join(root, "m3m4", src)
        if not os.path.isfile(sp): out.append(f"_derived_from 指向不存在的 {src}")
        else:
            want = transform(json.load(open(sp, encoding="utf-8")))
            if any(want[k] != o[k] for k in ("request", "status", "response")): out.append(f"与 m3m4 源样例 {src} 按规则派生的结果不一致")
    for p in ais(o):
        if p["schema"][-2:] != [F_MRP, F_MIA]: out.append("anthropic_identity.schema 末尾应为 metadata_rollout_percent、max_inflight_per_anchor")
        s = p["stored"]["spec"]
        if s.get("metadata_rollout_percent", 0) > s.get("rollout_percent", 0): out.append("metadata_rollout_percent 不得超过 rollout_percent")
    return out
scope = json.load(open(os.path.join(md, "SCOPE.json"), encoding="utf-8"))
samples = sorted(f for f in os.listdir(md) if f.endswith(".json") and f != "SCOPE.json")
for f in samples:
    for p in check(json.load(open(os.path.join(md, f), encoding="utf-8"))): print("PROBLEM", f, p); bad += 1
for f in scope["derived"]:
    if "m4l04_" + f not in samples: print("MISSING_DERIVED", f); bad += 1
inv = sorted(os.listdir(os.path.join(md, "invalid")))
for f in inv:
    got = check(json.load(open(os.path.join(md, "invalid", f), encoding="utf-8")))
    print(f"invalid {f}: {'REJECTED — ' + got[0] if got else 'ACCEPTED'}")
    if not got: bad += 1
print("frozen", nf, "samples", len(samples), "derived_sources", len(scope["derived"]), "unchanged_sources", len(scope["unchanged"]), "invalid", len(inv), "problems", bad)
sys.exit(1 if bad else 0)
