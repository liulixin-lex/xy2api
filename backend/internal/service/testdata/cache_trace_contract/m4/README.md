# cache_trace_contract/m4 · M4 追加：T2.2 L-04 本地锚点（v1.0）

> 作者：F，2026-10-24。依据：M4 开工包 §9（a2 规范）、§10（PM 裁定 X 的 FX-L04-1..7）；DESIGN §4.1；RULES §9。
> **与其他目录的关系**：M1、m3 与 m2b（v1.2.1）的文件一个字节都没改，sha256 记在 `FROZEN.sha256`（132 个文件）。本目录是 T2.2 构建（包含 T0b.4）的形态，v 仍为 1。L-03（T2.3）、L-07（T2.5）的字段在各自开工时再追加。

## 1. 字段（Anthropic/Gemini 完整事件，T2.2 构建中必有）
| 字段 | 取值 | 规则 |
|---|---|---|
| `l04_arm` | none / off / l04_only / both | 分组之前跳过时为 none；对照组为 off；处理组为 l04_only。both（L-03 也生效）只在能力 l03 为 true 且 h < metadata_rollout_percent 时出现，T2.2 中不会出现 |
| `l04_applied` | bool | 当且仅当 `l04_skip=none` 时为 true |
| `l04_skip` | §2 的枚举 | 按判定顺序取第一个命中的原因 |
| `l04_policy_rev` | int ≥0 | `cache_anthropic_identity_policy` 的 rev |
| `id_source` 增加 `anchor_v2` | — | 当且仅当 L-04 生效时为 anchor_v2。Gemini 的 a2 为空、落到 digest 链时为 `gemini_digest` |

- OpenAI 事件与最小事件不变。

## 2. 判定顺序（命中第一条即停）
| 阶段 | 原因 | 含义 |
|---|---|---|
| 分组之前（arm=none） | `policy_off` | kill、总开关关闭、anthropic_identity 不生效。能力 l04 为 false 的构建没有 l04_* 字段，所以不单列 capability 一项 |
| | `count_tokens` | count_tokens 一律不进入 L-04（§10.1） |
| | `no_api_key` | 鉴权后的 apiKey.ID ≤ 0（§10.7-1） |
| | `not_anthropic_group` | 请求时 serving 组的平台不是 anthropic（Gemini 锚点为相应的 Gemini 平台），例如保存后被改为 composite（§10.7-2） |
| | `group_unresolved` | claude_code_only 回退时解析不出有效 serving 组（§10.1） |
| | `not_in_group` | serving 组不在 `anchor_group_ids` 中；Gemini 为 `gemini_anchor=false` |
| | `share_active` | share active 的分组整体走 share 原逻辑（INV-16），id_source=share_anchor |
| | `empty_anchor` | 规范化后 system 与首条 user 都为空，a2 为空、不分桶（§9.2） |
| 分组 | `arm_off` | h ≥ rollout_percent |
| 分组之后（arm=l04_only/both） | `earlier_tier` | metadata 档、cacheable_content 档或 Gemini CLI 档先命中，L-04 不使用；id_source 为相应来源 |
| | `scope_mismatch` | 使用处 AnchorV2.servingGroupID 与当前 serving 组不同，视为空值走原兜底（§10.1） |
| | `fanin_cap` | 同一 a2 的在途数 ≥ max_inflight_per_anchor，或计数器异常、容量已满（§10.3） |
| 生效 | `none` | L-04 生效 |

- 分组之后的原因按意向分析计入处理组，与 L-01 的 client_pck 相同。

## 3. 分桶与 digest
- **分桶**：`h = sha256SumCacheCohort("anth-cohort|" + AnchorV2.value)`。输入是候选 AnchorV2，按 serving 组作用域，跨轮稳定（§9.4、§10.2）。分桶**独立于观测**：观测开关不得改变 L-04 的任何决策，需用 golden 证明。
- **与 T0b.4 digest 的关系**：
  - trace 的 `session_digest` 按 m2b v1.2.1 计算，取 `cacheIdentityDigest("<platform>-session", api_key_id|serving_group_id|本地会话哈希)`；
  - 观测关闭或该平台不在 platforms 中时，digest 不计算、为空（零执行）；
  - L-04 任何地方都不读 digest（§10.7-3），a2 也不得把 digest 当作输入或后备。
  - L-04 生效时，本地会话哈希就是 a2，所以 digest 由 a2 派生，仍按 (api_key, serving 组) 隔离。

## 4. 换组路径与 C2/C3 的断言口径（§10.1）
- **三条换组路径都要重新派生**，内容包括重算 AnchorV2、重做名单与分桶判定、重算 sessionHash：
  1. #125 中间件：天然满足；
  2. invalid_request（prompt-too-long）回退：gateway_handler.go:1025–1063，关闭时字节不变；
  3. claude_code_only 回退：在计算 a2 之前，用选号时的同一个纯判定函数解析有效 serving 组，解析不了记 `group_unresolved`。
- **C2/C3 的断言**（E-4 结项后并入，开工包 §10.1）：claude_code_only 回退场景下，同时断言 sticky 写入的组命名空间，即 `sticky_session:{groupID}:…` 中的 groupID 为解析后的 serving 组；覆盖 Layer3 等待路径（C2）与 profit gate 路径（C3）两种情形。trace 中的 `group_id` 也为该 serving 组。

## 5. 文件与自检
| 文件 | 内容 |
|---|---|
| `cache_trace.v1.m4.schema.json` | 由 m2b v1.2.1 的 schema 派生，加上 §1 的字段与 §2 的条件 |
| `examples/` | 10 个：Anthropic 8 个（生效、对照组、earlier_tier、not_in_group、policy_off、scope_mismatch、fanin_cap、share_active），Gemini 2 个（生效、empty_anchor 落到 digest 链） |
| `invalid/` | 9 个反例 |
| `FROZEN.sha256` | M1、m3 与 m2b 的 132 个文件 |
| `validate_trace_contract_m4.py` | 自检，依赖 jsonschema |

- 命令：`python3 -I m4/validate_trace_contract_m4.py <cache_trace_contract 目录>`，轻量，直接运行。
- **兼容**：m2b 中 OpenAI 事件与最小事件的样例在 m4 下仍然通过；m2b 的 Anthropic/Gemini 完整事件没有 l04_*，按设计被拒，因为 T2.2 构建中这些字段必有。
- 生成器：`07-design/scripts/gen_trace_contract_m4.py`。
