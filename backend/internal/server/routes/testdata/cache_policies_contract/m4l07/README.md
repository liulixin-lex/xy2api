# cache_policies_contract/m4l07 · T2.5 L-07 的策略增量（v1.0）

> 作者：F，2026-10-25。依据：M4 开工包 §11.1、DESIGN v1.7.8.15 §6.1A；m3m4 §3.6（能力位）；RULES §9。trace 侧见 `cache_trace_contract/m4l07/`。
> **与冻结内容的关系**：父目录（v1.7.4.6）与 `m3m4/`（v1.2）一个字节都没改，sha256 记在 `FROZEN.sha256`（132 个文件）。做法同 `m4l04/`。

## 1. 增量（routing.keep_sticky_on_busy，插在 rollout_percent 之后、affinity_mode 之前）
| 字段 | 定义 | 默认 | 方向 |
|---|---|---|---|
| `max_consecutive_busy` | int 1..5；cap_source `gateway.cache_routing.l07_max_consecutive_cap`（默认 5） | 2 | 调高为 enable |
| `max_hold_seconds` | int 10..300；cap_source `gateway.cache_routing.l07_max_hold_seconds_cap`（默认 300） | 60 | 调高为 enable |
| `owner_busy_skip_per_min_cap` | int 0..1000；0 表示自动取 max(4, 2×owner.Concurrency) | 0 | 非 0 时调高为 enable、调低为 disable；从非 0 改为 0 为 enable；从 0 改为非 0 为 disable |
| `group_keep_ratio_pct` | int 1..20，unit percent；cap_source `gateway.cache_routing.l07_group_keep_ratio_cap_pct`（默认 20） | 5 | 调高为 enable |
| `health_lookback_seconds` | int 30..600 | 120 | **相反**：调高为 disable（更保守），调低为 enable |

- **静态硬上限**：
  - 用既有的 `cap_source` 机制：schema 的 max 取当前上限。运行时值超过上限时，PUT 返回 400 CACHE_POLICY_INVALID，错误码 `exceeds_static_cap`，limit.max 为当前上限。
  - 重启后上限调低的，有效值取 min，并列入 effective.clipped_fields，与 observability 的 byte_budget_mib 相同。
  - 护栏参数 `l07_guard_error_delta_pp=5`、`l07_guard_pause_seconds=600` 只是静态配置，不进策略。
- 5 个字段都**不是能力字段**（m3m4 §3.6），l07 为 false 时也可以存储，只是不生效。
- **history 白名单**：5 个字段都加入 `cachePolicyHistoryField` 中 routing 的白名单。
- **旧存储补齐**：之前写入的存储值缺这些字段时，读取时在内存中补为默认值，不写回、rev 不变。
- **新的状态与提示**：
  - 护栏触发（A5）时，routing 的 state=blocked、active=false，blockers 带 `guard_tripped`（排在 not_implemented_in_build 之后）。到期自动恢复，不改存储的策略。
  - group_ids 含 share active 的分组时，给 warning `share_group_no_effect`（排在两个选项提示之前），允许写入。

## 2. 构建前提与样例选择
| 样例 | 构建能力 |
|---|---|
| 派生样例 `m4l07_m4_*`（9 个） | 沿用源样例的能力前提，只追加字段 |
| 新样例 `m4l07_*`（8 个，`_fixture.build_capabilities`） | **T2.5 构建**：l07=true；T2.2/T2.3 未合入时 l04、l03 为 false（anthropic_identity 为休眠形态） |

- T2.5 的 handler 回放以 8 个新样例为主。
- 构建同时包含 M2b、M3.1 或 T2.2 时，其他键按 `combo_t22/` 的规则叠加。这些增量作用于不同的键（或不同的嵌套字段），可以交换。

## 3. 新样例
| 样例 | 要点 |
|---|---|
| `m4l07_list_t25.json` | routing 已配置并生效，5 个字段为默认值 |
| `m4l07_list_clipped_static_cap.json` | 存储 300、上限调为 120：schema max=120，clipped_fields 列出该字段 |
| `m4l07_list_guard_tripped.json` | blocked + guard_tripped |
| `m4l07_validate_hold_raise.json` | 60→120 为 enable，要求理由、勾选与输入键名 |
| `m4l07_validate_health_lookback_raise.json` | 120→300 为 disable，不要求确认 |
| `m4l07_validate_owner_cap_auto.json` | 8→0（自动）为 enable |
| `m4l07_validate_share_group_warning.json` | 加入 share active 分组：share_group_no_effect |
| `m4l07_put_400_exceeds_static_cap.json` | 6 > 上限 5：exceeds_static_cap |

- `invalid/` 有 7 个反例。

## 4. 自检
- 命令：`python3 -I -B m4l07/validate_policies_m4l07.py <cache_policies_contract 目录>`，只用标准库，直接运行。
- 自检复用 m3m4 的全部规则，替换 routing 的字段集与默认值，并加入：
  - 两条特殊方向规则；
  - guard_tripped、share_group_no_effect；
  - “存储值超过 schema max 当且仅当列入 clipped_fields”；
  - 派生样例可以重新生成。
- 生成器：`07-design/scripts/gen_policies_m4l07.py`。

## 5. 各方动作
- **O-L07（实现）**：按本目录、`cache_trace_contract/m4l07/` 与 DESIGN §6.1A 实现。能力位 l07 只有在开工包 §11 全部实现后才能置为 true；accessor 与能力位做 AND。
- **UI**：routing 卡片增加 5 个整数字段，按 schema 渲染（max 可能因静态上限而变小）；health_lookback_seconds 的方向与其他字段相反，由服务端 validate 判定；显示 guard_tripped 与 share_group_no_effect 的文案。
