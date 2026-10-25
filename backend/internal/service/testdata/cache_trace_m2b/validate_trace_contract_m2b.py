#!/usr/bin/env python3
"""cache_trace M2b 追加契约自检（F）。只读、轻量，按 RULES §8 直接运行。
用法：python3 -I validate_trace_contract_m2b.py <cache_trace_contract 目录>
- M1 与 m3 的冻结文件逐个与 m2b/FROZEN.sha256 相等；
- m2b schema 合法；m2b/examples 全部通过，m2b/invalid 全部被拒；
- 兼容：m3 的全部合法样例、M1 的最小事件样例在 m2b schema 下仍然通过。"""
import hashlib, json, os, sys
from jsonschema import Draft202012Validator
d = sys.argv[1]; md = os.path.join(d, 'm2b'); bad = 0; nf = 0
for line in open(os.path.join(md, 'FROZEN.sha256'), encoding='utf-8'):
    want, path = line.split(None, 1); path = path.strip(); nf += 1
    if hashlib.sha256(open(os.path.join(d, path), 'rb').read()).hexdigest() != want:
        print('FROZEN_CHANGED', path); bad += 1
schema = json.load(open(os.path.join(md, 'cache_trace.v1.m2b.schema.json'), encoding='utf-8'))
Draft202012Validator.check_schema(schema); v = Draft202012Validator(schema)
def load(p): return json.load(open(p, encoding='utf-8'))['payload']
def semantic(e):
    # v1.1：schema 无法表达的两条字段间规则
    out = []
    if e.get('origin_group_id', 0) > 0 and e.get('origin_group_id') == e.get('group_id'):
        out.append('origin_group_id 与 group_id 相同')
    # v1.2（FX-OBS-1）：OpenAI 路径的计费解析不读别名 prompt_cache_hit_tokens（bit1），只有 bit1 时 read 未知
    uf = e.get('usage_fields')
    if e.get('platform') == 'openai' and e.get('transport') != 'plugin' and isinstance(uf, int) and (uf & 15) == 2 and e.get('loss_class') != 'n/a_usage':
        out.append('usage_fields 只有 bit1（别名）的 OpenAI 事件应判为 n/a_usage')
    lo, hi = e.get('read_blk_lo', -1), e.get('read_blk_hi', -1)
    if lo >= 0 and hi >= 0 and lo > hi:
        out.append('read_blk_lo 大于 read_blk_hi')
    return out
def errs(p):
    e = load(p)
    return [x.message for x in v.iter_errors(e)] + semantic(e)
ex = sorted(os.listdir(os.path.join(md, 'examples'))); inv = sorted(os.listdir(os.path.join(md, 'invalid')))
for n in ex:
    e = errs(os.path.join(md, 'examples', n))
    if e: print('EXAMPLE_FAIL', n, e[0][:160]); bad += 1
for n in inv:
    if not errs(os.path.join(md, 'invalid', n)): print('INVALID_ACCEPTED', n); bad += 1
m3ex = sorted(os.listdir(os.path.join(d, 'm3', 'examples')))
for n in m3ex:
    e = errs(os.path.join(d, 'm3', 'examples', n))
    if e: print('M3_EXAMPLE_FAIL_UNDER_M2B', n, e[0][:120]); bad += 1
m1all = sorted(os.listdir(os.path.join(d, 'examples')))   # v1.0.1：M1 的全部合法样例（M1 形态）在 m2b 下也通过
for n in m1all:
    e = errs(os.path.join(d, 'examples', n))
    if e: print('M1_EXAMPLE_FAIL_UNDER_M2B', n, e[0][:120]); bad += 1
rej = [n for n in sorted(os.listdir(os.path.join(d, 'examples'))) if n.startswith('rejected_')]
for n in rej:
    if errs(os.path.join(d, 'examples', n)): print('M1_MINIMAL_FAIL_UNDER_M2B', n); bad += 1
v11 = [n for n in ex if '_v11_' in n]   # v1.1：三组字段与拆分类都要有合法样例
cover = {k for n in v11 for k in load(os.path.join(md, 'examples', n))}
for k in ('first_user_blk', 'fork_in_kind', 'origin_group_id'):
    if k not in cover: print('V11_GROUP_NOT_EXERCISED', k); bad += 1
lcs = {load(os.path.join(md, 'examples', n)).get('loss_class') for n in v11}
for k in ('prefix_changed_client', 'prefix_changed_gateway'):
    if k not in lcs: print('V11_LOSS_CLASS_NOT_EXERCISED', k); bad += 1
plat = {load(os.path.join(md, 'examples', n)).get('platform') for n in ex}
for p in ('anthropic', 'gemini', 'openai'):
    if p not in plat: print('PLATFORM_NOT_EXERCISED', p); bad += 1
# v1.2.1：session_digest 向量（Anthropic/Gemini 按 api_key 与 serving 组隔离）
import hmac, hashlib
vec = json.load(open(os.path.join(md, 'session_digest_vectors.json'), encoding='utf-8'))
def scoped(e): return hmac.new(vec['secret'].encode(), (e['platform'] + '-session\x00' + f"{e['api_key_id']}|{e['serving_group_id']}|{e['local_session_hash']}").encode(), hashlib.sha256).hexdigest()[:16]
for e in vec['valid']:
    if scoped(e) != e['expected']: print('VECTOR_MISMATCH', e); bad += 1
groups = {}
for e in vec['valid']: groups.setdefault((e['platform'], e['local_session_hash']), set()).add(e['expected'])
for k, vals in groups.items():
    n = sum(1 for e in vec['valid'] if (e['platform'], e['local_session_hash']) == k)
    if len(vals) != n: print('VECTOR_NOT_ISOLATED', k); bad += 1
for e in vec['invalid']:
    ok = scoped(e) == e['claimed']
    print(f"invalid vector {e['platform']} key={e['api_key_id']}: {'ACCEPTED' if ok else 'REJECTED — 未按 (api_key, serving 组) 计算'}")
    if ok: bad += 1
print('frozen', nf, 'm2b_examples', len(ex), 'm2b_invalid', len(inv), 'm3_examples_under_m2b', len(m3ex), 'm1_examples_under_m2b', len(m1all), 'problems', bad)
sys.exit(1 if bad else 0)
