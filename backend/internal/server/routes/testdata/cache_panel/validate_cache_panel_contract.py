#!/usr/bin/env python3
"""cache_panel_contract 自检（F，M2）。只读、轻量，按 RULES §8 直接运行。

用法：python3 -I validate_cache_panel_contract.py <cache_panel_contract 目录>
- schema 本身合法；
- 顶层样例全部通过：schema（按 request.path 与 status 选择形态）+ 语义规则 + 隐私扫描；
- invalid/ 中的样例全部被拒（至少一条问题）。
"""
import json, os, re, sys
from datetime import datetime, timedelta
from urllib.parse import urlsplit, parse_qs
from jsonschema import Draft202012Validator

BASE = "/api/v1/admin/cache/"
ALLOWED = {"northstar": {"window", "platform", "granularity", "cost_view"},
           "breakdown": {"window", "platform", "dimension", "sort", "limit", "cost_view"},
           "stats": {"window", "platform"}, "experiments": set()}
ENUMS = {"platform": {"all", "openai", "anthropic", "gemini", "grok"}, "cost_view": {"list", "charged", "upstream"},
         "granularity": {"1h", "1d"}, "sort": {"opportunity", "opportunity_amount", "prompt", "token_hit"},
         "dimension": {"group", "account", "upstream_host", "client_family", "protocol", "model"}}
FORBIDDEN_KEYS = {"session_digest", "request_id_hmac", "conv_anchor", "user_id", "api_key_id", "api_key", "email",
                  "prompt_cache_key", "user_agent", "session_id", "ip_address", "is_fallback", "token"}
FORBIDDEN_VALUES = [re.compile(p) for p in (r"^v2:[0-9a-f]{16}$", r"^[0-9a-f]{32,}$", r"[^@\s]+@[^@\s]+\.[A-Za-z]+", r"^sk-")]
TOL = 5e-4

def ts(s): return datetime.fromisoformat(s.replace("Z", "+00:00"))
def ratio(n, d): return None if d == 0 else n / d

def privacy(o, path, out):
    if isinstance(o, dict):
        for k, v in o.items():
            if k in FORBIDDEN_KEYS:
                out.append(f"隐私：出现禁止字段 {path}{k}")
            privacy(v, f"{path}{k}.", out)
    elif isinstance(o, list):
        for i, v in enumerate(o):
            privacy(v, f"{path}{i}.", out)
    elif isinstance(o, str):
        for rx in FORBIDDEN_VALUES:
            if rx.search(o):
                out.append(f"隐私：{path.rstrip('.')} 的值形如标识或联系方式")

def check_metrics(m, where, out):
    p, rd, rq = m.get("prompt_tokens"), m.get("read_tokens"), m.get("requests")
    if p == 0:
        for k in ("token_hit_upper", "token_hit_lower"):
            if m.get(k) is not None: out.append(f"{where}.{k}：prompt_tokens=0 时应为 null")
    if rq == 0 and m.get("request_hit") is not None:
        out.append(f"{where}.request_hit：requests=0 时应为 null")
    if rd is not None and p is not None:
        if rd > p: out.append(f"{where}：read_tokens 大于 prompt_tokens")
        want = ratio(rd, p)
        if want is not None and m.get("token_hit_upper") is not None and abs(m["token_hit_upper"] - want) > TOL:
            out.append(f"{where}.token_hit_upper 与 read/prompt 不一致")
    u, l = m.get("token_hit_upper"), m.get("token_hit_lower")
    if u is not None and l is not None and l > u + 1e-12:
        out.append(f"{where}：token_hit_lower 大于 token_hit_upper")

def check_cost_notes(cost, view, where, out, full=True):
    notes = cost["notes"]
    if cost["view"] != view: out.append(f"{where}.view 应回显请求的 cost_view={view}")
    if (view != "list") != ("cost_weighted_hit_uses_list" in notes):
        out.append(f"{where}.notes：cost_weighted_hit_uses_list 当且仅当 cost_view≠list")
    if cost["pricing_changed_in_window"] != ("pricing_changed_in_window" in notes):
        out.append(f"{where}：pricing_changed_in_window 布尔与 notes 不一致")
    if not full: return
    if (cost["upstream_covered_p_share"] is not None) != (view == "upstream"):
        out.append(f"{where}.upstream_covered_p_share 只在 cost_view=upstream 时非 null")
    if (cost["priced_p_share"] < 0.95) != ("partial_pricing" in notes):
        out.append(f"{where}.notes：partial_pricing 当且仅当 priced_p_share<0.95")
    if view == "upstream" and cost["upstream_covered_p_share"] is not None and \
            (cost["upstream_covered_p_share"] < 0.95) != ("upstream_partial_coverage" in notes):
        out.append(f"{where}.notes：upstream_partial_coverage 当且仅当 upstream_covered_p_share<0.95")
    if cost["priced_models"] == 0:
        for k in ("saved_amount", "opportunity_amount", "token_hit_cost_weighted_upper", "token_hit_cost_weighted_lower"):
            if cost[k] is not None: out.append(f"{where}.{k}：没有可定价模型时应为 null")

def semantic(kind, q, data, out):
    if data["window"]["end"] != data["as_of"]:
        out.append("window.end 应等于 as_of（§1.2）")
    if kind == "northstar":
        win = q.get("window", "7d")
        gran = q.get("granularity") or ("1d" if win == "30d" else "1h")
        if data["granularity"] != gran: out.append(f"granularity 应为 {gran}")
        if data["platform"] != q.get("platform", "all"): out.append("platform 应回显请求值")
        s = data["summary"]; check_metrics(s, "summary", out)
        view = q.get("cost_view", "list")
        if s["cost"] is not None: check_cost_notes(s["cost"], view, "summary.cost", out)
        step = timedelta(days=1) if gran == "1d" else timedelta(hours=1)
        prev = None
        for i, pnt in enumerate(data["series"]):
            check_metrics(pnt, f"series[{i}]", out)
            b = ts(pnt["bucket_start"])
            if not (b + step > ts(data["window"]["start"]) and b < ts(data["window"]["end"])):
                out.append(f"series[{i}].bucket_start 不在窗口内")
            if prev is not None and b <= prev: out.append("series 应按 bucket_start 升序")
            prev = b
            if s["cost"] is None and any(pnt[k] is not None for k in ("saved_amount", "opportunity_amount", "token_hit_cost_weighted_upper")):
                out.append(f"series[{i}]：summary.cost 为 null 时金额字段应为 null")
    elif kind == "breakdown":
        for k, dflt in (("dimension", None), ("sort", "opportunity"), ("platform", "all")):
            if data[k] != q.get(k, dflt): out.append(f"{k} 应回显请求值")
        limit = int(q.get("limit", 10))
        if len(data["rows"]) > limit: out.append(f"rows 超过 limit={limit}")
        if data["cost"] is not None: check_cost_notes(data["cost"], q.get("cost_view", "list"), "cost", out, full=False)
        for i, row in enumerate(data["rows"]):
            check_metrics(row, f"rows[{i}]", out)
            m = row["meta"]
            if isinstance(m, dict) and data["dimension"] == "model" and m.get("priced") is False:
                if m.get("w_m") is not None: out.append(f"rows[{i}].meta.w_m：priced=false 时应为 null")
                if any(row[k] is not None for k in ("saved_amount", "opportunity_amount", "opportunity_amount_share", "token_hit_cost_weighted_upper")):
                    out.append(f"rows[{i}]：未定价模型的金额应为 null")
        if data["other"] is not None: check_metrics(data["other"], "other", out)
    elif kind == "stats":
        dur = {"1h": timedelta(hours=1), "24h": timedelta(hours=24)}[q.get("window", "24h")]
        if ts(data["window"]["end"]) - ts(data["window"]["start"]) != dur: out.append("window 长度与请求不符")
        eff = max(ts(data["window"]["start"]), ts(data["process_started_at"]))
        if ts(data["effective_start"]) != eff: out.append("effective_start 应为 max(window.start, process_started_at)")
        for i, lc in enumerate(data["loss_classes"]):
            na = lc["class"].startswith("n/a_")
            if lc["excluded"] != na: out.append(f"loss_classes[{i}]：excluded 当且仅当类别为 n/a_*")
            if na and lc["opportunity_tokens"] is not None: out.append(f"loss_classes[{i}]：n/a_* 的 opportunity_tokens 应为 null")
        c = data["coverage"]
        want = ratio(c["traced_prompt_tokens"], c["business_prompt_tokens"])
        if (want is None) != (c["coverage_ratio"] is None) or (want is not None and abs(c["coverage_ratio"] - want) > TOL):
            out.append("coverage.coverage_ratio 与 traced/business 不一致")

def check_sample(schema, o):
    out = []
    for k in ("_contract", "_note", "request", "status", "response"):
        if k not in o: return [f"缺少 {k}"]
    u = urlsplit(o["request"]["path"])
    if o["request"]["method"] != "GET" or not u.path.startswith(BASE):
        return ["只允许 GET /api/v1/admin/cache/*"]
    kind = u.path[len(BASE):]
    if kind not in ALLOWED: return [f"未知接口 {kind}"]
    raw = parse_qs(u.query, keep_blank_values=True)
    q = {k: v[0] for k, v in raw.items()}
    status = o["status"]
    bad_query = any(k not in ALLOWED[kind] or len(v) != 1 or v[0] == "" for k, v in raw.items()) or \
        any(k in ENUMS and q[k] not in ENUMS[k] for k in q) or \
        ("window" in q and q["window"] not in ({"1h", "24h"} if kind == "stats" else {"24h", "7d", "30d"})) or \
        ("limit" in q and not (q["limit"].isdigit() and 1 <= int(q["limit"]) <= 50)) or \
        (kind == "breakdown" and "dimension" not in q)
    if bad_query and status not in (400, 403):
        out.append("请求参数非法，应返回 400")
    ref = {200: kind, 400: "error_400", 403: "error_403", 503: "error_503"}.get(status)
    if ref is None: return out + [f"本契约不定义 HTTP {status}"]
    if status == 503 and kind not in ("northstar", "breakdown"): out.append("503 只用于 northstar 与 breakdown")
    v = Draft202012Validator({**schema, "$ref": f"#/$defs/{ref}"})
    errs = sorted(v.iter_errors(o["response"]), key=lambda e: list(e.path))
    out += [f"schema：{'/'.join(map(str, e.path)) or '(root)'}：{e.message[:120]}" for e in errs]
    if not errs and status == 200:
        semantic(kind, q, o["response"]["data"], out)
    privacy(o["response"], "", out)
    return out

def main(d):
    schema = json.load(open(os.path.join(d, "cache_panel.v1.schema.json"), encoding="utf-8"))
    Draft202012Validator.check_schema(schema)
    problems, nv, ni = [], 0, 0
    for f in sorted(os.listdir(d)):
        if not f.endswith(".json") or f.endswith(".schema.json"): continue
        nv += 1
        for p in check_sample(schema, json.load(open(os.path.join(d, f), encoding="utf-8"))):
            problems.append(f"{f}: {p}")
    for f in sorted(os.listdir(os.path.join(d, "invalid"))):
        ni += 1
        got = check_sample(schema, json.load(open(os.path.join(d, "invalid", f), encoding="utf-8")))
        print(f"invalid {f}: {'REJECTED' if got else 'ACCEPTED'}{(' — ' + got[0]) if got else ''}")
        if not got: problems.append(f"invalid/{f}: 应被拒绝却通过")
    for p in problems: print("PROBLEM", p)
    print(f"samples {nv} invalid {ni} problems {len(problems)}")
    return 1 if problems else 0

if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
