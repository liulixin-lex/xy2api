#!/usr/bin/env python3
"""cache_trace M4 追加契约（T2.3 L-03）自检（F）。只读、轻量，按 RULES §8 直接运行。依赖 jsonschema。
用法：python3 -I -B validate_trace_contract_m4l03.py <cache_trace_contract 目录>
- M1、m3、m2b 与 m4 的文件逐个与 m4l03/FROZEN.sha256 相等；
- m4l03 schema 合法；examples 全部通过，invalid 全部被拒；
- 兼容：m4 中的 Gemini 事件、m2b 中的 OpenAI 与最小事件在 m4l03 下仍然通过（m4 的 Anthropic 事件没有 l03_*，按设计被拒）；
- 判定顺序中的代表性原因都有样例。"""
import hashlib, json, os, sys
sys.dont_write_bytecode = True
from jsonschema import Draft202012Validator
d = sys.argv[1]; md = os.path.join(d, "m4l03"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, path = line.split(None, 1); path = path.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(d, path), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", path); bad += 1
schema = json.load(open(os.path.join(md, "cache_trace.v1.m4l03.schema.json"), encoding="utf-8"))
Draft202012Validator.check_schema(schema); v = Draft202012Validator(schema)
pv = Draft202012Validator(schema["$defs"]["admitted_provider"] | {"$defs": schema["$defs"]})
def load(p): return json.load(open(p, encoding="utf-8"))["payload"]
def errs(p):
    e = load(p)
    out = [x.message for x in v.iter_errors(e)]
    if out and e.get("platform") in ("anthropic", "gemini") and "owner_skip_reason" in e:   # 给出 provider 定义下的具体原因
        out = [x.message for x in pv.iter_errors(e)] or out
    return out
ex = sorted(os.listdir(os.path.join(md, "examples"))); inv = sorted(os.listdir(os.path.join(md, "invalid")))
for n in ex:
    e = errs(os.path.join(md, "examples", n))
    if e: print("EXAMPLE_FAIL", n, e[0][:160]); bad += 1
for n in inv:
    got = errs(os.path.join(md, "invalid", n))
    print(f"invalid {n}: {'REJECTED — ' + got[0][:110] if got else 'ACCEPTED'}")
    if not got: bad += 1
compat = 0
for n in sorted(os.listdir(os.path.join(d, "m4", "examples"))):
    if load(os.path.join(d, "m4", "examples", n)).get("platform") == "anthropic": continue
    compat += 1
    if errs(os.path.join(d, "m4", "examples", n)): print("M4_EXAMPLE_FAIL_UNDER_M4L03", n); bad += 1
for n in sorted(os.listdir(os.path.join(d, "m2b", "examples"))):
    e = load(os.path.join(d, "m2b", "examples", n))
    if e.get("platform") in ("anthropic", "gemini") and "owner_skip_reason" in e: continue
    compat += 1
    if errs(os.path.join(d, "m2b", "examples", n)): print("M2B_EXAMPLE_FAIL_UNDER_M4L03", n); bad += 1
got = {load(os.path.join(md, "examples", n)).get("l03_skip") for n in ex}
for k in ("none", "arm_off", "not_in_metadata_group", "policy_off", "client_user_id", "claude_cli_ua", "official_endpoint", "not_in_metadata_accounts", "fanin_cap"):
    if k not in got: print("SKIP_NOT_EXERCISED", k); bad += 1
print("frozen", nf, "m4l03_examples", len(ex), "m4l03_invalid", len(inv), "compat_examples", compat, "problems", bad)
sys.exit(1 if bad else 0)
