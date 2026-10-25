#!/usr/bin/env python3
"""cache_policies_contract/m4l03 自检（F）。只读、轻量，按 RULES §8 直接运行，只用标准库。
用法：python3 -I -B validate_policies_m4l03.py <cache_policies_contract 目录>
- 父目录（v1.7.4.6）、m3m4（v1.2）与 m4l04（v1.0）的文件逐个与 m4l03/FROZEN.sha256 相等；
- 复用 m3m4 的自检规则，anthropic_identity 的字段集换成 T2.3 的（m4l04 的两个字段，加 metadata_account_ids），并加入：
  metadata_account_ids 是 l03 的能力字段、空表示不生效；错误码 official_endpoint、not_apikey、exceeds_rollout_percent；
  账号候选与 account_options_unavailable 只管 metadata_account_ids，分组候选与 group_options_unavailable 只管分组字段；
  validate 的 diff 按字段名的字典序排列（v1.0.2）；
- 派生样例可由 m4l04 源样例重新生成；m4l04 中未变化的样例在本目录规则下同样通过；invalid/ 全部被拒。"""
import copy, hashlib, importlib.util, json, os, sys
sys.dont_write_bytecode = True
root = sys.argv[1]; md = os.path.join(root, "m4l03"); bad = 0; nf = 0
for line in open(os.path.join(md, "FROZEN.sha256"), encoding="utf-8"):
    want, p = line.split(None, 1); p = p.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(root, p), "rb").read()).hexdigest() != want: print("FROZEN_CHANGED", p); bad += 1
spec = importlib.util.spec_from_file_location("m3m4v", os.path.join(root, "m3m4", "validate_policies_m3m4.py"))
V = importlib.util.module_from_spec(spec); spec.loader.exec_module(V)
AI = {}
for k, v in V.DEFAULTS["anthropic_identity"].items():
    AI[k] = v
    if k == "metadata_group_ids": AI["metadata_account_ids"] = []
AI.update(metadata_rollout_percent=0, max_inflight_per_anchor=2)
V.DEFAULTS["anthropic_identity"] = AI
V.CODES.update({"exceeds_rollout_percent", "official_endpoint", "not_apikey"})
V.CAP_FIELDS["anthropic_identity"]["metadata_account_ids"] = "l03"
V.ACTIVATION["metadata_account_ids"] = lambda v: bool(v)
V.UNSUPPORTED_FIELDS.add("metadata_account_ids")
V.EMPTY_MEANS_OFF.add("metadata_account_ids")
ACCT = ("anthropic_identity", "metadata_account_ids")
_orig_policy = V.check_policy
def check_policy(p, master_on, out, where, caps=V.FULL_CAPS):
    """账号候选单独核对：先去掉 metadata_account_ids 的 options 与 account_options_unavailable，再交给 m3m4 的规则。"""
    if p.get("key") != "anthropic_identity" or not p.get("available"): return _orig_policy(p, master_on, out, where, caps)
    q = copy.deepcopy(p); ws = p["effective"].get("warnings", [])
    fs = [f["field"] for f in p["schema"]]
    if "metadata_account_ids" not in fs: out.append(f"{where}.anthropic_identity: T2.3 构建必须带 metadata_account_ids"); return _orig_policy(p, master_on, out, where, caps)
    f = p["schema"][fs.index("metadata_account_ids")]
    qf = q["schema"][fs.index("metadata_account_ids")]; qf.pop("options", None)
    q["effective"]["warnings"] = [w for w in ws if w != "account_options_unavailable"]
    _orig_policy(q, master_on, out, where, caps)
    if fs.index("metadata_account_ids") != fs.index("metadata_group_ids") + 1: out.append(f"{where}.anthropic_identity: metadata_account_ids 紧跟在 metadata_group_ids 之后")
    if {k: v for k, v in f.items() if k not in ("options", "not_implemented_in_build")} != {"field": "metadata_account_ids", "type": "id_list", "ref": "account"}:
        out.append(f"{where}.anthropic_identity.metadata_account_ids: 形状为 id_list、ref=account")
    opts = f.get("options")
    if not isinstance(opts, list): out.append(f"{where}.anthropic_identity.metadata_account_ids: FE-Q3 要求下发 options")
    else:
        if "account_options_unavailable" in ws and opts: out.append(f"{where}.anthropic_identity.metadata_account_ids: 带 account_options_unavailable 时 options 必须为 []")
        if len(opts) > 500: out.append(f"{where}.anthropic_identity.metadata_account_ids: options 超过 500 个")
        if [o.get("id") for o in opts] != sorted(o.get("id") for o in opts): out.append(f"{where}.anthropic_identity.metadata_account_ids: options 应按 id 升序")
        for o in opts:
            if set(o) != {"id", "name", "platform"} or o.get("platform") != "anthropic": out.append(f"{where}.anthropic_identity.metadata_account_ids: 选项只含 id、name、platform，且只列 anthropic 平台")
    if "account_options_unavailable" in ws:
        tail = [w for w in ws if w in ("account_options_unavailable", "group_options_unavailable")]
        if ws[-len(tail):] != tail or tail[0] != "account_options_unavailable": out.append(f"{where}.anthropic_identity: 选项提示追加在末尾，account_options_unavailable 在 group_options_unavailable 之前")
V.check_policy = check_policy
def insert_after(d, key, k, v):
    out = {}
    for a, b in d.items():
        out[a] = b
        if a == key: out[k] = v
    return out
ACCT_OPTS = [{"id": 501, "name": "relay-claude-a", "platform": "anthropic"}, {"id": 502, "name": "relay-claude-b", "platform": "anthropic"}]
def transform(o):
    o = copy.deepcopy(o); l03 = V.caps_of(o)["l03"]
    def walk(x):
        if isinstance(x, dict):
            if x.get("key") == "anthropic_identity" and x.get("available") and isinstance(x.get("schema"), list):
                f = {"field": "metadata_account_ids", "type": "id_list", "ref": "account", "options": copy.deepcopy(ACCT_OPTS)}
                if not l03: f["not_implemented_in_build"] = True
                i = [g["field"] for g in x["schema"]].index("metadata_group_ids") + 1
                x["schema"] = x["schema"][:i] + [f] + x["schema"][i:]
                x["defaults"] = insert_after(x["defaults"], "metadata_group_ids", "metadata_account_ids", [])
                x["stored"]["spec"] = insert_after(x["stored"]["spec"], "metadata_group_ids", "metadata_account_ids", x["stored"]["spec"].get("metadata_account_ids", []))
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
def check(o, derived_dir="m4l04"):
    out = V.check(o)
    src = o.get("_derived_from")
    if src:
        sp = os.path.join(root, derived_dir, src)
        if not os.path.isfile(sp): out.append(f"_derived_from 指向不存在的 {src}")
        elif any(transform(json.load(open(sp, encoding="utf-8")))[k] != o[k] for k in ("request", "status", "response")): out.append(f"与 m4l04 源样例 {src} 按规则派生的结果不一致")
    for p in ais(o):
        s = p["stored"]["spec"]
        if s.get("metadata_rollout_percent", 0) > s.get("rollout_percent", 0): out.append("metadata_rollout_percent 不得超过 rollout_percent")
        if not set(s.get("metadata_group_ids", [])) <= set(s.get("anchor_group_ids", [])): out.append("存储值中 metadata_group_ids ⊆ anchor_group_ids")
    d = o["response"].get("data") if isinstance(o.get("response"), dict) else None
    if isinstance(d, dict) and isinstance(d.get("diff"), list):   # v1.0.2：diff 按字段名（点路径）的字典序排列，与 m3m4 起的全部样例及实现（sort.Strings）一致
        fs = [x.get("field") for x in d["diff"]]
        if fs != sorted(fs): out.append("diff 应按字段名的字典序排列")
    if isinstance(d, dict) and "errors" in d:
        for e in d["errors"]:
            if e.get("code") in ("official_endpoint", "not_apikey") and e.get("field") != "metadata_account_ids": out.append(f"{e['code']} 只用于 metadata_account_ids")
    return out
scope = json.load(open(os.path.join(md, "SCOPE.json"), encoding="utf-8"))
samples = sorted(f for f in os.listdir(md) if f.endswith(".json") and f != "SCOPE.json")
for f in samples:
    for p in check(json.load(open(os.path.join(md, f), encoding="utf-8"))): print("PROBLEM", f, p); bad += 1
for f in scope["derived"]:
    if "m4l03_" + f not in samples: print("MISSING_DERIVED", f); bad += 1
for f in scope["unchanged_in_m4l04"]:   # 没有 anthropic_identity 条目的 m4l04 样例原样适用
    for p in check(json.load(open(os.path.join(root, "m4l04", f), encoding="utf-8"))): print("PROBLEM", "m4l04/" + f, p); bad += 1
inv = sorted(os.listdir(os.path.join(md, "invalid")))
for f in inv:
    got = check(json.load(open(os.path.join(md, "invalid", f), encoding="utf-8")))
    print(f"invalid {f}: {'REJECTED — ' + got[0] if got else 'ACCEPTED'}")
    if not got: bad += 1
print("frozen", nf, "samples", len(samples), "derived_sources", len(scope["derived"]), "unchanged_sources", len(scope["unchanged_in_m4l04"]), "invalid", len(inv), "problems", bad)
sys.exit(1 if bad else 0)
