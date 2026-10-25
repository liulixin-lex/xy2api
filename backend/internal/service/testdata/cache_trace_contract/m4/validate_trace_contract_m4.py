#!/usr/bin/env python3
"""cache_trace M4 追加契约自检（F）。只读、轻量，按 RULES §8 直接运行。依赖 jsonschema（与 m3、m2b、m31 相同）。
用法：python3 -I validate_trace_contract_m4.py <cache_trace_contract 目录>
- M1、m3 与 m2b（v1.2.1）的文件逐个与 m4/FROZEN.sha256 相等；
- m4 schema 合法；m4/examples 全部通过，m4/invalid 全部被拒；
- 兼容：OpenAI 事件与最小事件不受影响，m2b 中 platform=openai 的样例与全部最小事件样例在 m4 下仍然通过；
  m2b 的 Anthropic/Gemini 完整事件没有 l04_*，在 m4 下按设计被拒（T2.2 构建的形态）。"""
import hashlib, json, os, sys
from jsonschema import Draft202012Validator
d = sys.argv[1]; md = os.path.join(d, "m4"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, path = line.split(None, 1); path = path.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(d, path), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", path); bad += 1
schema = json.load(open(os.path.join(md, "cache_trace.v1.m4.schema.json"), encoding="utf-8"))
Draft202012Validator.check_schema(schema); v = Draft202012Validator(schema)
def load(p): return json.load(open(p, encoding="utf-8"))["payload"]
def errs(p): return [x.message for x in v.iter_errors(load(p))]
ex = sorted(os.listdir(os.path.join(md, "examples"))); inv = sorted(os.listdir(os.path.join(md, "invalid")))
for n in ex:
    e = errs(os.path.join(md, "examples", n))
    if e: print("EXAMPLE_FAIL", n, e[0][:160]); bad += 1
for n in inv:
    got = errs(os.path.join(md, "invalid", n))
    print(f"invalid {n}: {'REJECTED — ' + got[0][:100] if got else 'ACCEPTED'}")
    if not got: bad += 1
compat = 0
for n in sorted(os.listdir(os.path.join(d, "m2b", "examples"))):
    e = load(os.path.join(d, "m2b", "examples", n))
    if e.get("platform") in ("anthropic", "gemini") and "owner_skip_reason" in e:   # Anthropic/Gemini 完整事件：T2.2 构建中必有 l04_*
        continue
    compat += 1
    if errs(os.path.join(d, "m2b", "examples", n)): print("M2B_EXAMPLE_FAIL_UNDER_M4", n); bad += 1
skips = {load(os.path.join(md, "examples", n)).get("l04_skip") for n in ex}
for k in ("none", "arm_off", "earlier_tier", "scope_mismatch", "fanin_cap", "not_in_group", "empty_anchor"):
    if k not in skips: print("SKIP_NOT_EXERCISED", k); bad += 1
print("frozen", nf, "m4_examples", len(ex), "m4_invalid", len(inv), "m2b_examples_still_valid", compat, "problems", bad)
sys.exit(1 if bad else 0)
