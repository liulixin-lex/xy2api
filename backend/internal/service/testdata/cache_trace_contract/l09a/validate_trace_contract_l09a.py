#!/usr/bin/env python3
"""cache_trace T3.1a L-09a 追加契约自检（F）。只读、轻量，按 RULES §8 直接运行。依赖 jsonschema。
用法：python3 -I -B validate_trace_contract_l09a.py <cache_trace_contract 目录>
- M1、m3 与 m31 的文件逐个与 l09a/FROZEN.sha256 相等；schema 合法；examples 全部通过，invalid 全部被拒；
- 兼容：M1 的最小事件（rejected_*）在 l09a 下仍然通过（m31 的 admitted 样例没有 l09a_*，按设计被拒）；判定顺序的代表性原因都有样例。"""
import hashlib, json, os, sys
sys.dont_write_bytecode = True
from jsonschema import Draft202012Validator
d = sys.argv[1]; md = os.path.join(d, "l09a"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, path = line.split(None, 1); path = path.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(d, path), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", path); bad += 1
schema = json.load(open(os.path.join(md, "cache_trace.v1.l09a.schema.json"), encoding="utf-8"))
Draft202012Validator.check_schema(schema); v = Draft202012Validator(schema)
def load(p): return json.load(open(p, encoding="utf-8"))["payload"]
def errs(p): return [x.message for x in v.iter_errors(load(p))]
ex = sorted(os.listdir(os.path.join(md, "examples"))); inv = sorted(os.listdir(os.path.join(md, "invalid")))
for n in ex:
    e = errs(os.path.join(md, "examples", n))
    if e: print("EXAMPLE_FAIL", n, e[0][:200]); bad += 1
for n in inv:
    got = errs(os.path.join(md, "invalid", n))
    print(f"invalid {n}: {'REJECTED — ' + got[0][:100] if got else 'ACCEPTED'}")
    if not got: bad += 1
m1 = [n for n in sorted(os.listdir(os.path.join(d, "examples"))) if n.startswith("rejected_")]
for n in m1:
    if errs(os.path.join(d, "examples", n)): print("M1_EXAMPLE_FAIL_UNDER_L09A", n); bad += 1
got = {load(os.path.join(md, "examples", n)).get("l09a_skip") for n in ex}
for k in ("none", "arm_off", "policy_off", "excluded_path", "not_first_selection", "sticky_present", "no_hint", "hint_unusable", "no_same_host_candidate"):
    if k not in got: print("SKIP_NOT_EXERCISED", k); bad += 1
print("frozen", nf, "l09a_examples", len(ex), "l09a_invalid", len(inv), "m1_minimal_examples", len(m1), "problems", bad)
sys.exit(1 if bad else 0)
