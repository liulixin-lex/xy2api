# cache_trace_contract/m4l03 · M4 追加：T2.3 L-03 metadata.user_id（v1.0）

> 作者：F，2026-10-25。依据：M4 开工包 v1.3.2 §9.2、§10.2–§10.4、§10.8；DESIGN v1.7.8.16 §4.2；RULES §9。PM 决定 T2.3 现在开工，叠在 T2.2 上。
> **与其他目录的关系**：M1、m3、m2b（v1.2.1）与 m4（v1.0）的文件一个字节都没改，sha256 记在 `FROZEN.sha256`（156 个文件）。本目录是 T2.2 加 T2.3 构建的形态，v 仍为 1。

## 1. 字段（只出现在 Anthropic 完整事件，T2.3 构建中必有）
| 字段 | 取值 | 规则 |
|---|---|---|
| `l03_arm` | none / off / on | 分组之前跳过时为 none；L-03 的对照臂（h ≥ metadata_rollout_percent）为 off；处理臂为 on。**l03_arm=on 当且仅当 l04_arm=both** |
| `l03_applied` | bool | 当且仅当 `l03_skip=none` 时为 true，表示本请求的上游请求写入了我方生成的 metadata.user_id |
| `l03_skip` | §2 的枚举 | 按判定顺序取第一个命中的原因 |

- Gemini、OpenAI 事件与最小事件不带 l03_*；Gemini 的 l04_arm 不会是 both。
- **按最终 attempt 记录**：L-03 在每次 attempt 构造上游请求时判定（账号可能因 failover 改变），trace 记产生本事件的那次 attempt 的结果，与事件中的账号一致。
- 策略 rev 沿用 `l04_policy_rev`（同一个键 anthropic_identity）。
- L-03 不改本地粘性与选号（INV-19），`id_source` 不因 L-03 改变。

## 2. 判定顺序（命中第一条即停）
| 阶段 | 原因 | 含义 |
|---|---|---|
| 分组之前（arm=none） | 与 L-04 相同的 8 个原因 | `policy_off`、`count_tokens`、`no_api_key`、`not_anthropic_group`、`group_unresolved`、`not_in_group`、`share_active`、`empty_anchor`。**L-04 在分组之前跳过时，l03_skip 记相同的值**（含开工包 v1.3.2 §9.2：Responses 与 Chat 兼容入口记 empty_anchor） |
| | `not_in_metadata_group` | serving 组在 anchor_group_ids 中，但不在 metadata_group_ids 中（空表示不生效） |
| 分组 | `arm_off` | h ≥ metadata_rollout_percent（包括 L-04 的对照组 h ≥ rollout_percent） |
| 分组之后（arm=on） | `client_user_id` | 客户端带了非空的 metadata.user_id，不论能否解析，一律不覆盖（INV-02，客户端值优先） |
| | `mpt_enabled` | metadata 透传设置 `enableMPT` 为 true |
| | `claude_cli_ua` | UA 匹配 `claude-cli/X.Y.Z`（大小写不敏感，与 isClaudeCodeClient 的 UA 判定相同；开工包 §10.4，PM 确认） |
| | `scope_mismatch` | AnchorV2 的作用域与当前 serving 组不同（此时 l04_skip 也是 scope_mismatch） |
| | `fanin_cap` | 同一 a2 的在途数达到上限或计数器异常（此时 l04_skip 也是 fanin_cap，§10.3） |
| | `not_apikey` | 最终 attempt 的账号不是 apikey 账号（OAuth 等账号走既有改写） |
| | `official_endpoint` | 最终 attempt 的账号 base_url 为 api.anthropic.com（运行时硬排除，§10.4） |
| | `not_in_metadata_accounts` | 最终 attempt 的账号不在 metadata_account_ids 中（空表示不生效） |
| 生效 | `none` | 写入 metadata.user_id |

- 分组之后的原因按意向分析计入处理臂，与 L-04 的 earlier_tier 相同。
- **与 L-04 的组合**：L-04 因 cacheable_content 档先命中而记 earlier_tier 时，L-03 仍可生效（a2 照常计算，开工包 §9.4）；客户端 user_id 合法、本地走 metadata 档（id_source=metadata_user_id）时，L-03 只能记 client_user_id；L-04 记 scope_mismatch 或 fanin_cap 时，L-03 不生效。

## 3. 写入的值（开工包 v1.3.1 §10.4）
- `FormatMetadataUserID(device_id, "", session_id, "")`，即旧格式 `user_<64 hex>_account__session_<36>`；
- `device_id` 为 HMAC-SHA256(K, `anth-device|` + serving_account_id) 的完整 64 位小写 hex；`session_id = generateSessionUUID("anthropic-upstream-session:v1:" + a2)`；`account_uuid` 为空；
- 一次 `sjson.SetBytes("metadata.user_id")`，不写 `X-Claude-Code-Session-Id`；值只写上游（INV-18），不进 trace 与日志（trace 只记上面三个枚举字段）。

## 4. 文件与自检
| 文件 | 内容 |
|---|---|
| `cache_trace.v1.m4l03.schema.json` | 由 m4 的 schema 派生：加入 l03_* 与 §2 的条件 |
| `examples/` | 11 个：生效、earlier_tier 时生效、对照臂、not_in_metadata_group、与 L-04 同因（policy_off）、client_user_id、claude_cli_ua、official_endpoint、not_in_metadata_accounts、fanin_cap、Gemini 不带 l03_* |
| `invalid/` | 13 个反例 |
| `FROZEN.sha256` | M1、m3、m2b 与 m4 的 156 个文件 |
| `validate_trace_contract_m4l03.py` | 自检，依赖 jsonschema |

- 命令：`python3 -I -B m4l03/validate_trace_contract_m4l03.py <cache_trace_contract 目录>`，轻量，直接运行。
- **兼容**：m4 的 Gemini 样例、m2b 的 OpenAI 与最小事件样例在 m4l03 下仍然通过；m4 的 Anthropic 样例没有 l03_*，按设计被拒。
- **与 m4l07 组合**（构建同时含 T2.5）：Anthropic 完整事件同时带 l03_* 与 l07_*，两组字段与条件互不相交，取两份 schema 的并集；l04_* 在 m4l07 中为全有或全无，在含 T2.2 的构建中按必有处理。
- 生成器：`07-design/scripts/gen_trace_contract_m4l03.py`。
