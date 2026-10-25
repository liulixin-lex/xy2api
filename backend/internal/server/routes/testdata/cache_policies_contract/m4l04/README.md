# cache_policies_contract/m4l04 · T2.2 L-04 的策略增量（v1.0）

> 作者：F，2026-10-24。依据：M4 开工包 §10.2（嵌套阈值）、§10.3（汇聚上限）；m3m4 §3.6（能力位）；RULES §9。trace 侧见 `cache_trace_contract/m4/`。
> **与冻结内容的关系**：父目录（v1.7.4.6）与 `m3m4/`（v1.2）一个字节都没改，sha256 记在 `FROZEN.sha256`（132 个文件）。做法同 `m31/`：派生样例加新样例。

## 1. 增量（anthropic_identity，追加在 schema 末尾）
| 字段 | 定义 | 默认 | 方向 | 校验 |
|---|---|---|---|---|
| `metadata_rollout_percent` | int 0..100，unit percent | 0 | 调高为 enable，调低为 disable | 硬校验 ≤ `rollout_percent`，否则 `exceeds_rollout_percent`，limit.max 为请求后的 rollout_percent；出错的字段不进 diff |
| `max_inflight_per_anchor` | int 1..16 | 2 | 调高为 enable（放宽汇聚上限），调低为 disable | 范围外为 `out_of_range` |

- 两者都**不是能力字段**（m3m4 §3.6），l03 为 false 时 metadata_rollout_percent 也可以存储，只是不生效。
- **旧存储补齐**：之前写入的存储值缺这两个字段时，读取时在内存中补为默认值，不写回、rev 不变（同 m2m2b v1.0.1）。
- **history 白名单**：两个字段都加入 `cachePolicyHistoryField` 中 anthropic_identity 的白名单。
- `metadata_account_ids`（§10.4）属于 L-03，在 T2.3 开工时追加。
- 分桶（§10.2）：h < metadata_rollout_percent 时 L-03 与 L-04 都生效（需要 l03 为 true）；h < rollout_percent 时只有 L-04；其余为对照组。

## 2. 构建前提与样例选择
| 样例 | 构建能力 |
|---|---|
| 派生样例 `m4l04_m4_*`（7 个） | 沿用源样例：`m4_*` 为完整构建（三项都为 true），`m4_dormant_*` 为默认构建（三项都为 false）。只追加新字段，用于核对字段形态 |
| 新样例 `m4l04_*`（5 个，`_fixture.build_capabilities`） | **T2.2 构建**：l04=true，l03=false，l07=false；同时包含 T0b.4，Anthropic 观测参数为 true，所以没有 `anthropic_observability_missing` |

- **T2.2 的 handler 回放以 5 个新样例为主**。派生样例按 `_fixture` 预置相应的能力后回放。
- 只缺部分能力时（m3m4 §3.6）：L-04 已实现并被激活，state 按 L-04 计算；metadata_group_ids 带 `not_implemented_in_build`，开启方向报 unsupported_in_build。
- 构建同时包含 M3.1 时，upstream_identity 再按 `m31/` 追加 max_inflight_per_key。两者作用于不同的键，可以叠加。

## 3. 新样例
| 样例 | 要点 |
|---|---|
| `m4l04_list_t22.json` | L-04 生效；只有 metadata_group_ids 与 routing 的两个 L-07 字段带标记 |
| `m4l04_validate_metadata_rollout_exceeds.json` | 60 > rollout 50：`exceeds_rollout_percent`，limit.max=50 |
| `m4l04_validate_max_inflight_anchor_raise.json` | 2→4 为 enable，要求理由与勾选 |
| `m4l04_validate_metadata_dormant.json` | anchor 可以开启；metadata_group_ids 报 unsupported_in_build |
| `m4l04_put_anchor_enable_200.json` | PUT 开启 L-04，rev 1→2，响应中只有 metadata_group_ids 带标记 |

- `invalid/` 有 6 个反例。

## 4. 自检
- 命令：`python3 -I m4l04/validate_policies_m4l04.py <cache_policies_contract 目录>`，只用标准库，直接运行。
- 复用 m3m4 自检的全部规则（含 §3.6 的能力检查），只把 anthropic_identity 的字段集与默认值换成 T2.2 的，并加入错误码 `exceeds_rollout_percent`。另外核对派生样例可以重新生成，以及存储值中的 metadata_rollout_percent ≤ rollout_percent。
- 生成器：`07-design/scripts/gen_policies_m4l04.py`。

## 5. 各方动作
- **G**：T2.2 按本目录与 `cache_trace_contract/m4/` 实现，handler 测试以 §3 的样例为主；两个字段加入 history 白名单。
- **UI**：anthropic_identity 卡片增加两个整数字段，按 schema 渲染：metadata_rollout_percent 为百分比，并说明“L-03 未实现时不生效”；max_inflight_per_anchor 为 1..16。T2.2 构建中只有 metadata_group_ids 被锁定。
