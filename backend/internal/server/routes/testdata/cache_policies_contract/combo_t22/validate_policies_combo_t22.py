#!/usr/bin/env python3
"""cache_policies_contract/combo_t22 自检（F）。只读、轻量，按 RULES §8 直接运行，只用标准库。
用法：python3 -I validate_policies_combo_t22.py <cache_policies_contract 目录>
- 依赖的目录（父目录、m3m4、m2m2b、m31、m4l04）逐个文件与 combo_t22/FROZEN.sha256 相等；
- 组合样例可由 m4l04 的源样例按 README §1 的规则重新生成（用同目录的生成器同一份规则）；
- 复用 m3m4 的自检，把 observability、upstream_identity、anthropic_identity 的字段集与默认值换成组合后的；invalid/ 全部被拒。"""
import copy, hashlib, importlib.util, json, os, sys
sys.dont_write_bytecode = True   # 导入 m3m4 的自检时不在共享契约目录中写 __pycache__
root = sys.argv[1]; md = os.path.join(root, "combo_t22"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, p = line.split(None, 1); p = p.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(root, p), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", p); bad += 1
def mod(name, path):
    spec = importlib.util.spec_from_file_location(name, path); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m); return m
V = mod("m3m4v", os.path.join(root, "m3m4", "validate_policies_m3m4.py"))
V.OBS_M2 = ["enabled", "sample_percent", "chain_percent", "byte_budget_mib", "rate_mib_per_s", "platforms", "ingress_chain_percent", "persist_trace_hourly"]
V.DEFAULTS["upstream_identity"]["max_inflight_per_key"] = 2
V.DEFAULTS["anthropic_identity"].update(metadata_rollout_percent=0, max_inflight_per_anchor=2)
V.CODES.add("exceeds_rollout_percent")
V.WARNINGS.add("l01_all_accounts_unverified")
V.WARN_ORDER.insert(V.WARN_ORDER.index("account_options_unavailable"), "l01_all_accounts_unverified")
# 组合规则与生成器相同（README §1）
F_PLAT = {"field": "platforms", "type": "enum_list", "options": ["openai", "anthropic", "gemini", "messages_bridge"]}
F_ING = {"field": "ingress_chain_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"}
F_MAX = {"field": "max_inflight_per_key", "type": "int", "min": 1, "max": 16}
def insert_before(d, key, items):
    out = {}
    for k, v in d.items():
        if k == key:
            for ik, iv in items: out[ik] = iv
        out[k] = v
    return out
def compose(o):
    o = copy.deepcopy(o)
    def walk(x):
        if isinstance(x, dict):
            if x.get("available") and isinstance(x.get("schema"), list):
                if x.get("key") == "observability":
                    i = [f["field"] for f in x["schema"]].index("persist_trace_hourly")
                    x["schema"] = x["schema"][:i] + [copy.deepcopy(F_PLAT), copy.deepcopy(F_ING)] + x["schema"][i:]
                    x["defaults"] = insert_before(x["defaults"], "persist_trace_hourly", [("platforms", ["openai"]), ("ingress_chain_percent", 10)])
                    s = x["stored"]["spec"]
                    x["stored"]["spec"] = insert_before(s, "persist_trace_hourly", [("platforms", s.get("platforms", ["openai"])), ("ingress_chain_percent", s.get("ingress_chain_percent", min(10, s["chain_percent"])))])
                elif x.get("key") == "upstream_identity":
                    x["schema"] = x["schema"] + [copy.deepcopy(F_MAX)]
                    x["defaults"]["max_inflight_per_key"] = 2
                    x["stored"]["spec"].setdefault("max_inflight_per_key", 2)
                    s = x["stored"]["spec"]
                    if s.get("mode") == "uuid_fill" and s.get("account_ids") == []:
                        x["effective"]["warnings"] = sorted(set(x["effective"]["warnings"]) | {"l01_all_accounts_unverified"}, key=V.WARN_ORDER.index)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return o
def check(o):
    out = V.check(o)
    src = o.get("_composed_from")
    if src:
        want = compose(json.load(open(os.path.join(root, src), encoding="utf-8")))
        if any(want[k] != o[k] for k in ("request", "status", "response")): out.append(f"与 {src} 按组合规则派生的结果不一致")
    for p in o["response"]["data"].get("policies", []) if isinstance(o["response"].get("data"), dict) else []:
        if p.get("key") == "observability" and p.get("available"):
            s = p["stored"]["spec"]
            if s["ingress_chain_percent"] > s["chain_percent"]: out.append("ingress_chain_percent 不得超过 chain_percent")
    return out
scope = json.load(open(os.path.join(md, "SCOPE.json"), encoding="utf-8"))
samples = sorted(f for f in os.listdir(md) if f.endswith(".json") and f != "SCOPE.json")
for f in samples:
    for p in check(json.load(open(os.path.join(md, f), encoding="utf-8"))): print("PROBLEM", f, p); bad += 1
for f in scope["composed"]:
    if "combo_" + f not in samples: print("MISSING_COMPOSED", f); bad += 1
inv = sorted(os.listdir(os.path.join(md, "invalid")))
for f in inv:
    got = check(json.load(open(os.path.join(md, "invalid", f), encoding="utf-8")))
    print(f"invalid {f}: {'REJECTED — ' + got[0] if got else 'ACCEPTED'}")
    if not got: bad += 1
print("frozen", nf, "samples", len(samples), "unchanged_in_m4l04", len(scope["unchanged_in_m4l04"]), "invalid", len(inv), "problems", bad)
sys.exit(1 if bad else 0)
