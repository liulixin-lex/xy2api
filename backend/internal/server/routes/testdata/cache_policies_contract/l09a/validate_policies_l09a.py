#!/usr/bin/env python3
"""cache_policies_contract/l09a 自检（F）。只读、轻量，按 RULES §8 直接运行，只用标准库。
用法：python3 -I -B validate_policies_l09a.py <cache_policies_contract 目录>
- 依赖目录（父目录、m3m4、m4l07）逐个文件与 l09a/FROZEN.sha256 相等；
- 以模块方式载入 m4l07 的自检（屏蔽输出，同时确认它通过），复用其已按 T2.5 配置的规则，再加入：
  affinity_mode 是能力 l09a 的能力字段（未声明时为 false）；prev_host+quality 始终 unsupported_in_build；affinity_rollout_percent；
  static_aware_routing_disabled 当且仅当 affinity 激活且静态 cache-aware 不可用（list 中按 existing 判定）；
- 派生样例可由 m4l07 源样例重新生成；diff 按字典序；invalid/ 全部被拒。"""
import contextlib, copy, hashlib, importlib.util, io, json, os, sys
sys.dont_write_bytecode = True
root = sys.argv[1]; md = os.path.join(root, "l09a"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, p = line.split(None, 1); p = p.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(root, p), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", p); bad += 1
spec = importlib.util.spec_from_file_location("m4l07v", os.path.join(root, "m4l07", "validate_policies_m4l07.py"))
M = importlib.util.module_from_spec(spec)
with contextlib.redirect_stdout(io.StringIO()):
    try: spec.loader.exec_module(M)
    except SystemExit as e:
        if e.code: print("M4L07_SELFCHECK_FAILED"); bad += 1
V = M.V
V.DEFAULTS["routing"]["affinity_rollout_percent"] = 0
V.FULL_CAPS["l09a"] = False
V.CAP_FIELDS["routing"]["affinity_mode"] = "l09a"
V.ACTIVATION["affinity_mode"] = lambda v: v != "score"
BO = V.BLOCKER_ORDER
if "stale_snapshot" not in BO: V.BLOCKERS.add("stale_snapshot"); BO.insert(BO.index("parse_error") + 1, "stale_snapshot")
_cap = V.cap_problems
def cap_problems(p, caps, where):
    res = _cap(p, caps, where)
    if "static_aware_routing_disabled" in p["effective"].get("blockers", []):   # 静态 cache-aware 不可用也使 routing 为 blocked（开工包 §5）
        res = [x for x in res if "只有在未实现的功能被激活时才为 blocked" not in x]
    return res
V.cap_problems = cap_problems
F_AR = {"field": "affinity_rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"}
def transform(o):
    o = copy.deepcopy(o); l09a = V.caps_of(o)["l09a"]
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "routing" and x.get("available") and isinstance(x.get("schema"), list):
                i = [f["field"] for f in x["schema"]].index("affinity_mode")
                if not l09a: x["schema"][i]["not_implemented_in_build"] = True
                x["schema"] = x["schema"][:i + 1] + [copy.deepcopy(F_AR)] + x["schema"][i + 1:]
                x["defaults"]["affinity_rollout_percent"] = 0
                x["stored"]["spec"].setdefault("affinity_rollout_percent", 0)
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(o); return o
def check(o):
    out = V.check(o)
    body = (o.get("request") or {}).get("body") or {}
    if (body.get("spec") or {}).get("affinity_mode") == "prev_host+quality":   # L-10 未实现：l09a 为 true 时仍报 unsupported_in_build
        out = [x for x in out if not (x.startswith("affinity_mode:") and "为 true 时不报 unsupported_in_build" in x)]
    src = o.get("_derived_from")
    if src and any(transform(json.load(open(os.path.join(root, "m4l07", src), encoding="utf-8")))[k] != o[k] for k in ("request", "status", "response")):
        out.append(f"与 m4l07 源样例 {src} 按规则派生的结果不一致")
    d = o["response"].get("data") if isinstance(o.get("response"), dict) else None
    if isinstance(d, dict):
        if isinstance(d.get("diff"), list) and [x["field"] for x in d["diff"]] != sorted(x["field"] for x in d["diff"]): out.append("diff 应按字段名的字典序排列")
        if body.get("spec", {}).get("affinity_mode") == "prev_host+quality" and o.get("status") == 200 and isinstance(d.get("errors"), list) \
           and {"field": "affinity_mode", "code": "unsupported_in_build"} not in d["errors"]:
            out.append("prev_host+quality 需要 L-10，在其实现之前必须报 unsupported_in_build")
        if isinstance(d.get("errors"), list) and o.get("status") == 200:
            for e in d["errors"]:
                if e["field"] == "affinity_mode" and e["code"] == "unsupported_in_build" and body.get("spec", {}).get("affinity_mode") == "prev_host" and V.caps_of(o)["l09a"]:
                    out.append("l09a 为 true 时 prev_host 不报 unsupported_in_build")
        if "policies" in d and "existing" in d:
            ex = d["existing"]; static_ok = ex.get("openai_cache_aware_routing_enabled") is True and ex.get("openai_cache_rollout_percent", 0) > 0
            for p in d["policies"]:
                if p["key"] != "routing" or not p["available"]: continue
                fs = [f["field"] for f in p["schema"]]
                if "affinity_rollout_percent" not in fs or fs.index("affinity_rollout_percent") != fs.index("affinity_mode") + 1: out.append("routing: affinity_rollout_percent 紧跟在 affinity_mode 之后")
                act = p["stored"]["spec"].get("affinity_mode", "score") != "score"
                killed = p["effective"]["state"] in ("killed", "parse_error")
                if not killed and (act and not static_ok) != ("static_aware_routing_disabled" in p["effective"]["blockers"]):
                    out.append("routing: static_aware_routing_disabled 当且仅当 affinity 激活且静态 cache-aware 不可用")
    return out
scope = json.load(open(os.path.join(md, "SCOPE.json"), encoding="utf-8"))
samples = sorted(f for f in os.listdir(md) if f.endswith(".json") and f != "SCOPE.json")
for f in samples:
    for p in check(json.load(open(os.path.join(md, f), encoding="utf-8"))): print("PROBLEM", f, p); bad += 1
for f in scope["derived"]:
    if "l09a_" + f not in samples: print("MISSING_DERIVED", f); bad += 1
inv = sorted(os.listdir(os.path.join(md, "invalid")))
for f in inv:
    got = check(json.load(open(os.path.join(md, "invalid", f), encoding="utf-8")))
    print(f"invalid {f}: {'REJECTED — ' + got[0] if got else 'ACCEPTED'}")
    if not got: bad += 1
print("frozen", nf, "samples", len(samples), "derived_sources", len(scope["derived"]), "unchanged_sources", len(scope["unchanged_in_m4l07"]), "invalid", len(inv), "problems", bad)
sys.exit(1 if bad else 0)
