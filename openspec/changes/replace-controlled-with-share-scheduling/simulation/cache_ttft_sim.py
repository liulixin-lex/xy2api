#!/usr/bin/env python3
"""份额调度的缓存命中与首字延迟（TTFT）参考仿真（仅标准库、固定种子、无网络、不读写文件）。

运行：python3 -I openspec/changes/replace-controlled-with-share-scheduling/simulation/cache_ttft_sim.py

离散事件仿真，比较以下账号选择策略在同一负载、同一组故障/运维事件下的表现：
  sub2api      原版 Gateway 路径：粘性命中→粘性排队（≤3 人、120 秒）→优先级/负载率/LRU→兜底排队；
               负载均衡层选中后覆盖粘性绑定；账号限流即删除绑定；“停用”用不可调度开关表达。
  share_v1     2026-10-09 规划版份额调度：HRW+亲和、绑定满载立即临时溢出（wait_ms=0）、
               高层连续合格 120 秒即迁回（stable）、失败/不可用即改绑、排空到期强制迁移。
  share_v2     本次修订（默认值）：按缓存价值等待绑定账号；满载溢出即改绑到会话 HRW 次选；
               短时不可用且恢复时缓存仍热才软溢出（保留绑定）；回升高层、排空、权重变化
               只在缓存已冷（空闲≥缓存 TTL 或会话无可缓存前缀）时迁移；排空到期强制。
  share_v2_strict  同上，但回升高层另设 30 分钟强制截止（按会话错峰 0–600 秒）。
  per_request  无亲和的逐请求加权随机（份额最精确，作为缓存下界参照）。

模型假设（均为参数，可改）：
  - 上游缓存按账号隔离；命中要求同一账号、同一会话前缀、未过期；寿命从读写请求开始计；
    共享前缀（工具+系统提示）跨会话共享。
  - TTFT = 排队等待 + 基础延迟 + 未缓存输入×单价 + 缓存输入×单价 + 冷连接惩罚（账号空闲>90 秒）。
  - 成本指数按输入计：缓存写 1.25、缓存读 0.10（Anthropic 5 分钟写入价）；1 小时 TTL 场景写入 2.0。
这些系数只用于比较策略之间的相对差异，不代表任何上游的真实延迟或价格。
"""
import hashlib
import heapq
import math
import random
import statistics
import struct
import sys
from collections import deque

SIM_SEEDS = (11, 23, 37, 41, 59)
MINUTE = 60.0

# --- 上游与延迟模型 ---------------------------------------------------------
MIN_CACHEABLE = 1024
SHARED_PREFIX = 14000          # 工具 + 系统提示，同客户端画像的会话共享
BASE_MS = 800.0
UNCACHED_MS_PER_TOKEN = 0.05   # 每 1k 未缓存输入 50ms
CACHED_MS_PER_TOKEN = 0.005    # 每 1k 缓存输入 5ms
COLD_CONN_MS = 250.0
CONN_IDLE_S = 90.0
GEN_TOKENS_PER_S = 70.0
STICKY_TTL = 3600.0

# --- 账号：id, 层级(=原版 priority), 权重, 并发上限 ---------------------------
ACCOUNTS = (
    ("A", 1, 5, 8), ("B", 1, 3, 6), ("C", 1, 2, 4),
    ("D", 2, 1, 4), ("E", 2, 1, 4), ("F", 2, 1, 4),
)

# --- 负载：会话到达（每分钟），高峰制造短时满载 -----------------------------
BASE_RATE = 2.5
PEAKS = ((30, 45, 5.0), (195, 205, 5.0))
ARRIVAL_END_MIN = 220
SIM_END_MIN = 330

# --- 事件（分钟）-------------------------------------------------------------
EV_SHORT_OUTAGE = (70, ("A",), 180)            # A 限流 3 分钟
EV_TIER_OUTAGE = (110, ("A", "B", "C"), 600)   # 第 1 层整体限流 10 分钟
EV_WEIGHT = (150, "C", 4)                      # C 权重 2 -> 4（仅份额调度有权重）
EV_DRAIN = (170, "B", 15)                      # 排空 B，15 分钟后到期（原版用不可调度开关）

# --- share_v2 参数 -------------------------------------------------------------
# 等待预算 = min(max_wait_s, 预计可读缓存 Token / 1000 × ms_per_1k)；排队人数上限 max_waiting；
# 回升/排空的强制迁移截止 deadline_s，并按会话错峰 0–600 秒。
V2_DEFAULTS = dict(ms_per_1k=100.0, max_wait_s=15.0, max_waiting=3, deadline_s=None,
                   rebind_on_spill=True, realign_cold=True, soft_margin=0.8, soft_max_s=300.0)
STRATEGY_PARAMS = {
    "share_v2": {},
    "share_v2_strict": {"deadline_s": 1800.0},
}
V2_DEADLINE_JITTER_S = 600.0


def u01(*parts):
    raw = hashlib.blake2b("|".join(str(p) for p in parts).encode(), digest_size=8).digest()
    return (struct.unpack(">Q", raw)[0] + 1) / 2 ** 64


class Account:
    def __init__(self, name, tier, weight, conc):
        self.name, self.tier, self.weight, self.conc = name, tier, weight, conc
        self.inflight = 0
        self.queue = deque()
        self.waiting = 0
        self.last_used = -1e9
        self.last_end = -1e9
        self.rl_until = -1.0
        self.eligible_since = 0.0
        self.unschedulable = False
        self.drained = False
        self.drain_until = None
        self.sess_cache = {}     # sid -> (cached_len, epoch, expire)
        self.shared_exp = -1.0

    def eligible(self, t):
        return t >= self.rl_until and not self.unschedulable

    def free(self):
        return self.inflight < self.conc


class Session:
    def __init__(self, sid, rng, start):
        self.sid = sid
        self.start = start
        self.shared = rng.random() < 0.7
        sys_tokens = SHARED_PREFIX if self.shared else int(min(20000, rng.lognormvariate(math.log(3000), 0.8)))
        self.ctx = sys_tokens + int(min(30000, rng.lognormvariate(math.log(2000), 1.0)))
        self.epoch = 0
        turns = 1
        while rng.random() > 1 / 20 and turns < 120:
            turns += 1
        self.turns_left = turns
        self.rng = rng
        self.last_acct = None
        self.last_len = 0
        self.last_start = None
        self.jitter = u01("jitter", sid) * V2_DEADLINE_JITTER_S

    def think(self):
        if self.rng.random() < 0.12:
            return self.rng.uniform(240, 1200)
        return min(180.0, self.rng.lognormvariate(math.log(20), 0.7))

    def output_tokens(self):
        return int(min(4000, self.rng.lognormvariate(math.log(600), 0.6)))

    def grow(self, out_tokens):
        self.ctx += out_tokens + int(min(40000, self.rng.lognormvariate(math.log(2500), 1.0)))
        if self.ctx > 180000:          # 压缩：前缀改变，所有账号上的会话缓存失效
            self.ctx = 40000
            self.epoch += 1


class Request:
    __slots__ = ("sess", "t_arrive", "excluded", "state", "acct", "no_wait", "spill_reason", "reason")

    def __init__(self, sess, t):
        self.sess, self.t_arrive = sess, t
        self.excluded = set()
        self.state = "new"
        self.acct = None
        self.no_wait = False
        self.spill_reason = None
        self.reason = None


class Sim:
    def __init__(self, strategy, seed, cache_ttl, params=None):
        self.strategy, self.seed, self.cache_ttl = strategy, seed, cache_ttl
        self.p = dict(V2_DEFAULTS, **STRATEGY_PARAMS.get(strategy, {}), **(params or {}))
        self.select = getattr(self, "select_share_v2" if strategy.startswith("share_v2") else "select_" + strategy)
        self.write_mult = 1.25 if cache_ttl <= 300 else 2.0
        self.rng = random.Random(seed)
        self.acc = {n: Account(n, tr, w, c) for n, tr, w, c in ACCOUNTS}
        self.heap, self.seq = [], 0
        self.bind = {}               # sid -> [acct, last_seen]
        self.tier_recovered_at = 0.0
        self.m = dict(req=0, inp=0, read=0, write=0, ttft=[], ttft_long=[], fails=0, wait=[],
                      warm_switch={}, lost_read=0, new_sessions={}, tier2_healthy=0, healthy_req=0,
                      tier2_linger=0, lost_by={}, switch_by={}, req_share={})

    # ---------------- event plumbing ----------------
    def push(self, t, kind, payload):
        self.seq += 1
        heapq.heappush(self.heap, (t, self.seq, kind, payload))

    def run(self):
        t, sid = 0.0, 0
        while True:
            minute = t / MINUTE
            rate = BASE_RATE
            for lo, hi, r in PEAKS:
                if lo <= minute < hi:
                    rate = r
            t += self.rng.expovariate(rate / MINUTE)
            if t >= ARRIVAL_END_MIN * MINUTE:
                break
            sid += 1
            sess = Session(sid, random.Random(self.seed * 100003 + sid), t)
            self.push(t, "req", Request(sess, t))
        at, names, dur = EV_SHORT_OUTAGE
        self.push(at * MINUTE, "outage", (names, dur))
        at, names, dur = EV_TIER_OUTAGE
        self.push(at * MINUTE, "outage", (names, dur))
        self.push(EV_WEIGHT[0] * MINUTE, "weight", EV_WEIGHT[1:])
        self.push(EV_DRAIN[0] * MINUTE, "drain", EV_DRAIN[1:])
        while self.heap:
            t, _, kind, p = heapq.heappop(self.heap)
            if t > SIM_END_MIN * MINUTE:
                break
            getattr(self, "on_" + kind)(t, p)
        return self.m

    def on_outage(self, t, p):
        names, dur = p
        for n in names:
            a = self.acc[n]
            a.rl_until = t + dur
            a.eligible_since = t + dur
        if len(names) > 1:
            self.tier_recovered_at = t + dur

    def on_weight(self, t, p):
        name, w = p
        if self.strategy != "sub2api":
            self.acc[name].weight = w

    def on_drain(self, t, p):
        name, minutes = p
        a = self.acc[name]
        if self.strategy == "sub2api":
            a.unschedulable = True
        else:
            a.drained = True
            a.drain_until = t + minutes * MINUTE

    def on_req(self, t, r):
        r.state = "new"
        self.dispatch(t, r)

    def on_retry(self, t, r):
        self.dispatch(t, r)

    def on_timeout(self, t, r):
        if r.state != "waiting":
            return
        r.state = "timeout"
        r.acct.waiting -= 1
        if self.strategy.startswith("share_v2") and r.no_wait is False and r.spill_reason == "affinity_wait":
            r.no_wait = True
            r.excluded.add(r.acct.name)
            self.dispatch(t, r)
        else:
            self.m["fails"] += 1
            r.excluded = set()
            self.push(t + 2.0, "retry", r)

    def on_done(self, t, p):
        r, a, out = p
        a.inflight -= 1
        a.last_end = t
        s = r.sess
        s.grow(out)
        s.turns_left -= 1
        if s.turns_left > 0:
            nt = t + s.think()
            self.push(nt, "req", Request(s, nt))
        while a.queue and a.free():
            w = a.queue.popleft()
            if w.state == "waiting":
                a.waiting -= 1
                w.state = "started"
                self.start(t, w, a)

    # ---------------- selection ----------------
    def dispatch(self, t, r):
        decision = self.select(t, r)
        kind = decision[0]
        if kind == "serve":
            r.reason = decision[2]
            self.start(t, r, decision[1])
        elif kind == "wait":
            _, a, timeout, reason = decision
            r.state, r.acct, r.spill_reason, r.reason = "waiting", a, reason, reason
            a.queue.append(r)
            a.waiting += 1
            self.push(t + timeout, "timeout", r)
        else:
            self.m["fails"] += 1
            r.excluded = set()
            self.push(t + 2.0, "retry", r)

    def binding(self, t, sid):
        b = self.bind.get(sid)
        if b and t - b[1] <= STICKY_TTL:
            return b
        self.bind.pop(sid, None)
        return None

    def set_bind(self, t, sid, name):
        self.bind[sid] = [name, t]

    def hrw(self, sid, accounts):
        return sorted(accounts, key=lambda a: -math.log(u01("g1", sid, a.name)) / a.weight)

    def tiers(self):
        return sorted({a.tier for a in self.acc.values()})

    # 原版：粘性 → 优先级/负载率/LRU → 兜底排队
    def select_sub2api(self, t, r):
        s = r.sess
        b = self.binding(t, s.sid)
        if b:
            a = self.acc[b[0]]
            if not a.eligible(t):
                self.bind.pop(s.sid, None)          # shouldClearStickySession
            elif a.name not in r.excluded:
                if a.free():
                    b[1] = t
                    return ("serve", a, "affinity_hit")
                if a.waiting < 3:
                    b[1] = t
                    return ("wait", a, 120.0, "affinity_wait")
        why = "new_session" if s.last_acct is None else ("rebind_unavailable" if b and not self.acc[b[0]].eligible(t)
                                                          else "rebind_saturated" if b else "rebind_expired")
        avail = [a for a in self.acc.values() if a.eligible(t) and a.name not in r.excluded
                 and (a.inflight + a.waiting) < a.conc]
        while avail:
            minp = min(a.tier for a in avail)
            cands = [a for a in avail if a.tier == minp]
            minload = min((a.inflight + a.waiting) / a.conc for a in cands)
            cands = [a for a in cands if (a.inflight + a.waiting) / a.conc == minload]
            sel = min(cands, key=lambda a: (a.last_used, a.name))
            if sel.free():
                self.set_bind(t, s.sid, sel.name)
                return ("serve", sel, why)
            avail.remove(sel)
        cands = sorted((a for a in self.acc.values() if a.eligible(t)), key=lambda a: (a.tier, a.last_used))
        if cands:
            return ("wait", cands[0], 30.0, "fallback_wait")
        return ("fail",)

    def placeable(self, t, a, r):
        return a.eligible(t) and not a.drained and a.weight > 0 and a.name not in r.excluded

    def active_tier(self, t):
        for tier in self.tiers():
            if any(a.tier == tier and a.eligible(t) and not a.drained and a.weight > 0 for a in self.acc.values()):
                return tier
        return None

    def place_new(self, t, r, why=None):
        s = r.sess
        why = why or ("new_session" if s.last_acct is None else "rebind_expired")
        for tier in self.tiers():
            group = [a for a in self.acc.values() if a.tier == tier and self.placeable(t, a, r)]
            for a in self.hrw(s.sid, group):
                if a.free():
                    self.set_bind(t, s.sid, a.name)
                    return ("serve", a, why)
        tier = self.active_tier(t)
        if tier is None:
            return ("fail",)
        group = [a for a in self.acc.values() if a.tier == tier and self.placeable(t, a, r)]
        if not group:
            return ("fail",)
        first = self.hrw(s.sid, group)[0]
        self.set_bind(t, s.sid, first.name)
        return ("wait", first, 30.0, "fallback_wait")

    def spill(self, t, r, bound, why):
        """临时溢出：按会话 HRW 次序（本层优先）找有空位的账号，不改绑定；都满则排队。"""
        s = r.sess
        same_tier_first = sorted(self.tiers(), key=lambda tr: (tr != bound.tier, tr))
        for tier in same_tier_first:
            group = [a for a in self.acc.values() if a.tier == tier and a is not bound
                     and self.placeable(t, a, r)]
            for a in self.hrw(s.sid, group):
                if a.free():
                    if why == "spill_saturated" and self.strategy.startswith("share_v2") and self.p["rebind_on_spill"]:
                        self.set_bind(t, s.sid, a.name)
                    return ("serve", a, why)
        if bound.eligible(t) and bound.name not in r.excluded:
            return ("wait", bound, 30.0, "fallback_wait")
        tier = self.active_tier(t)
        group = [a for a in self.acc.values() if tier is not None and a.tier == tier and self.placeable(t, a, r)]
        if group:
            return ("wait", self.hrw(s.sid, group)[0], 30.0, "fallback_wait")
        return ("fail",)

    def stable_higher_tier(self, t, tier):
        return any(a.tier == tier and a.eligible(t) and not a.drained and t - a.eligible_since >= 120
                   for a in self.acc.values())

    # 2026-10-09 规划版
    def select_share_v1(self, t, r):
        s = r.sess
        b = self.binding(t, s.sid)
        if not b:
            return self.place_new(t, r)
        a = self.acc[b[0]]
        if not a.eligible(t) or a.name in r.excluded:
            return self.place_new(t, r, "rebind_unavailable")
        active = self.active_tier(t)
        if active is not None and a.tier > active and self.stable_higher_tier(t, active):
            return self.place_new(t, r, "return_to_higher_tier")
        if a.drained and t >= a.drain_until:
            return self.place_new(t, r, "drained_rebind")
        if a.free():
            b[1] = t
            return ("serve", a, "affinity_hit")
        b[1] = t
        return self.spill(t, r, a, "spill_saturated")

    # 本次修订
    def cache_cold(self, t, s, b):
        return (t - b[1]) >= self.cache_ttl or s.last_len < MIN_CACHEABLE

    def select_share_v2(self, t, r):
        s = r.sess
        b = self.binding(t, s.sid)
        if not b:
            return self.place_new(t, r)
        a = self.acc[b[0]]
        p = self.p
        if not a.eligible(t):
            # 软溢出：预计恢复时绑定账号上的缓存仍未过期，才保留绑定
            remaining = a.rl_until - t
            if (not a.unschedulable and remaining <= p["soft_max_s"]
                    and remaining + (t - b[1]) < p["soft_margin"] * self.cache_ttl):
                b[1] = t
                return self.spill(t, r, a, "spill_unavailable")
            return self.place_new(t, r, "rebind_unavailable")
        if a.name in r.excluded:
            return self.spill(t, r, a, "spill_saturated")
        active = self.active_tier(t)
        cold = self.cache_cold(t, s, b)
        if cold and p["realign_cold"] and active is not None:
            group = [x for x in self.acc.values() if x.tier == active and self.placeable(t, x, r)]
            if group and self.hrw(s.sid, group)[0] is not a:
                return self.place_new(t, r, "realign_cold")
        if active is not None and a.tier > active and self.stable_higher_tier(t, active):
            if cold:
                return self.place_new(t, r, "return_cold")
            if p["deadline_s"] is not None and t >= self.tier_recovered_at + p["deadline_s"] + s.jitter:
                return self.place_new(t, r, "return_deadline")
        if a.drained:
            if cold:
                return self.place_new(t, r, "drain_cold")
            if t >= a.drain_until + s.jitter:
                return self.place_new(t, r, "drain_deadline")
        if a.free():
            b[1] = t
            return ("serve", a, "affinity_hit")
        warm = (t - b[1]) < self.cache_ttl
        b[1] = t
        est = s.ctx if warm else 0                   # 实现中取请求体估算 Token 与 PTTL 推算的空闲时长
        budget = min(p["max_wait_s"], est / 1000.0 * p["ms_per_1k"] / 1000.0) if est >= MIN_CACHEABLE else 0.0
        if budget > 0 and not r.no_wait and a.waiting < p["max_waiting"]:
            return ("wait", a, budget, "affinity_wait")
        return self.spill(t, r, a, "spill_saturated")

    def select_per_request(self, t, r):
        for tier in self.tiers():
            group = [a for a in self.acc.values() if a.tier == tier and self.placeable(t, a, r) and a.free()]
            if group:
                total = sum(a.weight for a in group)
                x = self.rng.random() * total
                for a in sorted(group, key=lambda a: a.name):
                    x -= a.weight
                    if x <= 0:
                        return ("serve", a, "per_request")
                return ("serve", group[-1], "per_request")
        tier = self.active_tier(t)
        group = sorted((a for a in self.acc.values() if tier is not None and a.tier == tier
                        and self.placeable(t, a, r)), key=lambda a: a.name)
        return ("wait", group[0], 30.0, "fallback_wait") if group else ("fail",)

    # ---------------- service ----------------
    def start(self, t, r, a):
        s = r.sess
        r.state, r.acct = "started", a
        cold_conn = a.inflight == 0 and (t - a.last_end) > CONN_IDLE_S
        a.inflight += 1
        a.last_used = t
        L = s.ctx
        read = 0
        if L >= MIN_CACHEABLE:
            entry = a.sess_cache.get(s.sid)
            if entry and entry[1] == s.epoch and entry[2] >= t:
                read = min(entry[0], L)
            elif s.shared and a.shared_exp >= t:
                read = SHARED_PREFIX
            a.sess_cache[s.sid] = (L, s.epoch, t + self.cache_ttl)
            if s.shared:
                a.shared_exp = t + self.cache_ttl
        write = L - read if L >= MIN_CACHEABLE else 0
        # 换号统计：上一请求所在账号的会话缓存仍然有效却换到别的账号
        if s.last_acct is not None and s.last_acct != a.name and s.last_len >= MIN_CACHEABLE:
            prev = self.acc[s.last_acct].sess_cache.get(s.sid)
            if prev and prev[1] == s.epoch and prev[2] >= t:
                b = self.bind.get(s.sid)
                why = "spill" if (b and b[0] == s.last_acct) else "rebind"
                self.m["warm_switch"][why] = self.m["warm_switch"].get(why, 0) + 1
                lost = max(0, min(prev[0], L) - read)
                self.m["lost_read"] += lost
                self.m["lost_by"][r.reason] = self.m["lost_by"].get(r.reason, 0) + lost
                self.m["switch_by"][r.reason] = self.m["switch_by"].get(r.reason, 0) + 1
        if s.last_acct is None:
            self.m["new_sessions"].setdefault((self.phase(t), a.name), 0)
            self.m["new_sessions"][(self.phase(t), a.name)] += 1
        wait = t - r.t_arrive
        ttft = wait + (BASE_MS + (L - read) * UNCACHED_MS_PER_TOKEN + read * CACHED_MS_PER_TOKEN
                       + (COLD_CONN_MS if cold_conn else 0.0)) / 1000.0
        out = s.output_tokens()
        m = self.m
        m["req"] += 1
        m["inp"] += L
        m["read"] += read
        m["write"] += write
        m["ttft"].append(ttft)
        m["wait"].append(wait)
        if L >= 60000:
            m["ttft_long"].append(ttft)
        ph = self.phase(t)
        if ph != "other" and a.tier == 1:
            m["req_share"][(ph, a.name)] = m["req_share"].get((ph, a.name), 0) + 1
        if self.tier1_healthy(t):
            m["healthy_req"] += 1
            if a.tier == 2:
                m["tier2_healthy"] += 1
                b = self.bind.get(s.sid)
                if b and self.acc[b[0]].tier == 2:
                    m["tier2_linger"] += 1
        s.last_acct, s.last_len, s.last_start = a.name, L, t
        self.push(t + ttft - wait + out / GEN_TOKENS_PER_S, "done", (r, a, out))

    def tier1_healthy(self, t):
        return all(a.eligible(t) or a.unschedulable for a in self.acc.values() if a.tier == 1)

    @staticmethod
    def phase(t):
        minute = t / MINUTE
        if minute < EV_SHORT_OUTAGE[0] and not any(lo <= minute < hi for lo, hi, _ in PEAKS):
            return "w532"
        if EV_WEIGHT[0] + 1 <= minute < EV_DRAIN[0] and not any(lo <= minute < hi for lo, hi, _ in PEAKS):
            return "w534"
        return "other"


def pct(xs, q):
    xs = sorted(xs)
    if not xs:
        return float("nan")
    k = (len(xs) - 1) * q
    lo = math.floor(k)
    hi = min(lo + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


SCENARIOS = (
    ("运维事件场景（短时限流、整层限流 10 分钟、改权重、排空）", {}),
    ("稳态场景（无故障与运维事件，仅负载高峰）",
     dict(EV_SHORT_OUTAGE=(9999, ("A",), 180), EV_TIER_OUTAGE=(9999, ("A", "B", "C"), 600),
          EV_WEIGHT=(9999, "C", 4), EV_DRAIN=(9999, "B", 15))),
)
STRATEGIES = ("sub2api", "share_v1", "share_v2", "share_v2_strict", "per_request")


def run_rows(strategy, cache_ttl):
    rows = []
    for seed in SIM_SEEDS:
        m = Sim(strategy, seed, cache_ttl).run()
        write_mult = 1.25 if cache_ttl <= 300 else 2.0
        cost = (m["write"] * write_mult + m["read"] * 0.10 + (m["inp"] - m["write"] - m["read"])) / m["inp"]
        rows.append(dict(
            hit=m["read"] / m["inp"], cost=cost,
            p50=pct(m["ttft"], 0.5), p95=pct(m["ttft"], 0.95), p99=pct(m["ttft"], 0.99),
            long95=pct(m["ttft_long"], 0.95), wait95=pct(m["wait"], 0.95),
            lost=m["lost_read"] / m["inp"], fails=m["fails"],
            tier2=m["tier2_healthy"] / max(1, m["healthy_req"]),
            lost_by={k: v / m["inp"] for k, v in m["lost_by"].items()},
            ns=m["new_sessions"], rs=m["req_share"],
        ))
    return rows


def mean(rows, key):
    return statistics.fmean(r[key] for r in rows)


def share_line(rows, field, phase, names):
    totals = {n: 0 for n in names}
    for r in rows:
        for (ph, n), c in r[field].items():
            if ph == phase and n in totals:
                totals[n] += c
    s = sum(totals.values()) or 1
    return "/".join(f"{totals[n] / s * 100:.1f}" for n in names)


def lost_line(rows):
    keys = {}
    for r in rows:
        for k, v in r["lost_by"].items():
            keys[k] = keys.get(k, 0.0) + v / len(rows)
    top = sorted(keys.items(), key=lambda kv: -kv[1])[:5]
    return " ".join(f"{k}={v * 100:.2f}%" for k, v in top) or "-"


def main():
    defaults = {k: getattr(sys.modules[__name__], k) for k, _ in SCENARIOS[1][1].items()}
    for title, overrides in SCENARIOS:
        for k, v in defaults.items():
            setattr(sys.modules[__name__], k, v)
        for k, v in overrides.items():
            setattr(sys.modules[__name__], k, v)
        for ttl, label in ((300.0, "缓存 TTL 5 分钟"), (3600.0, "缓存 TTL 1 小时")):
            print(f"== {title}；{label}；{len(SIM_SEEDS)} 个种子均值 ==")
            print("策略              命中率  Δ原版   成本   P50    P95    P99   长上下文P95 等待P95 失效读  健康期第2层 失败")
            res = {st: run_rows(st, ttl) for st in STRATEGIES}
            base = mean(res["sub2api"], "hit")
            for st in STRATEGIES:
                rows = res[st]
                print(f"{st:<16}{mean(rows, 'hit') * 100:6.1f}% {(mean(rows, 'hit') - base) * 100:+5.1f}pp"
                      f" {mean(rows, 'cost'):5.3f} {mean(rows, 'p50'):5.2f}s {mean(rows, 'p95'):5.2f}s"
                      f" {mean(rows, 'p99'):5.2f}s {mean(rows, 'long95'):8.2f}s {mean(rows, 'wait95'):6.2f}s"
                      f" {mean(rows, 'lost') * 100:5.2f}% {mean(rows, 'tier2') * 100:9.2f}% {mean(rows, 'fails'):5.1f}")
            for st in ("share_v2", "share_v2_strict"):
                d_hit = [(a["hit"] - b["hit"]) * 100 for a, b in zip(res[st], res["sub2api"])]
                r95 = [a["p95"] / b["p95"] for a, b in zip(res[st], res["sub2api"])]
                r99 = [a["p99"] / b["p99"] for a, b in zip(res[st], res["sub2api"])]
                print(f"  {st} 对原版逐种子：命中率差 {min(d_hit):+.1f}~{max(d_hit):+.1f}pp；"
                      f"P95 比 {min(r95):.2f}~{max(r95):.2f}；P99 比 {min(r99):.2f}~{max(r99):.2f}")
            for st in ("sub2api", "share_v1", "share_v2"):
                print(f"  失效读按原因（{st}）：{lost_line(res[st])}")
            print("  第 1 层 A/B/C 份额%（健康非高峰；权重 5:3:2 → 50/30/20）：")
            for st in STRATEGIES:
                print(f"    {st:<16} 新会话 {share_line(res[st], 'ns', 'w532', 'ABC'):<16}"
                      f" 请求 {share_line(res[st], 'rs', 'w532', 'ABC')}")
            print()


if __name__ == "__main__":
    main()
