# cache_policies_contract/m4l03 · T2.3 L-03 的策略增量（v1.0.1）

> 作者：F，2026-10-25。依据：M4 开工包 v1.3.2 §10.4（FX-L04-4，含 A 的 sub2api 核对结论）、§10.2；DESIGN v1.7.8.16 §4.2；m3m4 §3.1、§3.6；RULES §9。trace 侧见 `cache_trace_contract/m4l03/`。
> **与冻结内容的关系**：父目录（v1.7.4.6）、`m3m4/`（v1.2）与 `m4l04/`（v1.0）一个字节都没改，sha256 记在 `FROZEN.sha256`（155 个文件）。T2.3 叠在 T2.2 上，所以样例由 m4l04 派生。

## 1. 增量：anthropic_identity.metadata_account_ids（插在 metadata_group_ids 之后）
| 项 | 规则 |
|---|---|
| 形状 | `{"field":"metadata_account_ids","type":"id_list","ref":"account","options":[…]}`，默认 `[]` |
| 含义 | **空表示不生效**，逐个账号显式开启，与 anchor_group_ids、metadata_group_ids 相同（同一个键里三个列表都是“空表示关闭”），与 upstream_identity 的 account_ids（空表示全部）相反 |
| 方向 | 新增 ID 为开启方向；移除 ID、改回空为关闭方向（m3m4 §3.1 的“空表示关闭”规则）。同时有增有减为 mixed |
| 校验（只核对**新增**的 ID；移除永远允许） | 不存在报 `unknown_account`；不是 anthropic 平台报 `wrong_platform`；base_url 为 api.anthropic.com 报 **`official_endpoint`**；不是 apikey 账号报 **`not_apikey`**；重复报 `duplicate`。前四个是语义错误，字段照常进入 diff；duplicate 属于解析阶段，字段不进入 diff（m3m4 规则）。都使 valid=false，PUT 返回 400 CACHE_POLICY_INVALID |
| 能力 | l03 的能力字段（m3m4 §3.6）：l03 为 false 时带 `not_implemented_in_build`，开启方向报 unsupported_in_build；存储为非空时 state=blocked |
| 候选（FE-Q3） | options 只列 anthropic 平台、非官方端点的 apikey 账号，按 id 升序，最多 500 个，元素只含 id、name、platform。账号候选查询失败或被跳过时 options 为 []，warnings 追加 `account_options_unavailable`。disable 方向的 PUT 不查询账号表，同样为 [] 并带该提示（与 v1.0.3 的 upstream_identity 相同） |
| 提示的作用范围 | m3m4 §3.4 的“一个键带提示时，它的所有 FE-Q3 字段都为 []”在本键细化为**按候选类型**：`account_options_unavailable` 只管 metadata_account_ids，`group_options_unavailable` 只管两个分组字段，两者都出现时 account 在前 |
| 候选未加载时的前端（v1.0.1，PM 2026-10-25） | 改为**手动输入 ID**；已存储的 ID 照常显示，由服务端在 PUT 时校验。**不**加载管理端的完整账号列表（可能有数千个账号，大多不符合条件）。与 v1.0.3 的 upstream_identity 相同 |
| history | 加入 `cachePolicyHistoryField` 中 anthropic_identity 的白名单 |
| 旧存储补齐 | 之前写入的存储值缺这个字段时，读取时在内存中补为 []，不写回、rev 不变 |

- **L-03 的生效条件**（全部满足，trace 见 m4l03 §2）：策略生效；serving 组在 metadata_group_ids（⊆ anchor_group_ids）中；h < metadata_rollout_percent（≤ rollout_percent）；客户端没有带非空的 metadata.user_id；enableMPT 为 false；UA 不是 claude-cli；a2 可用（非空、作用域一致、未超汇聚上限）；最终 attempt 的账号是 metadata_account_ids 中的非官方 apikey 账号。
- **运行时**：账号保存后 base_url 被改为官方端点、或类型改变时，不改存储，运行时跳过（trace 记 official_endpoint 或 not_apikey）。

## 2. 构建前提与样例
| 样例 | 构建能力 |
|---|---|
| 派生样例 `m4l03_m4l04_*`（9 个） | 沿用 m4l04 源样例的能力前提，只插入字段；l03 为 false 的样例中新字段带标记 |
| 新样例 `m4l03_*`（7 个，`_fixture.build_capabilities`） | **T2.3 构建**：l04、l03=true，l07=false；包含 T0b.4 |

| 新样例 | 要点 |
|---|---|
| `m4l03_list_t23.json` | L-04 与 L-03 都开启，metadata 两个字段不带标记，账号候选下发 |
| `m4l03_list_account_options_unavailable.json` | 账号候选为 []，带 account_options_unavailable；分组候选照常 |
| `m4l03_validate_metadata_enable.json` | 三个字段都是开启方向，要求理由与勾选 |
| `m4l03_validate_account_rejected.json` | 新增 503（官方端点）与 504（OAuth）：official_endpoint、not_apikey |
| `m4l03_validate_accounts_clear.json` | 清空为关闭方向，不要求确认；L-04 仍生效 |
| `m4l03_put_metadata_enable_200.json` | PUT 开启 L-03，rev 2→3 |
| `m4l03_put_400_official_endpoint.json` | 400 CACHE_POLICY_INVALID |

- m4l04 中 3 个 validate 样例没有 anthropic_identity 条目，原样适用（`SCOPE.json` 的 unchanged_in_m4l04），自检会在本目录规则下复核。
- `invalid/` 有 8 个反例。
- **组合**：构建同时包含 M2b、M3.1、T2.5 或 POL-2 时，按 `combo_t22/`、`m4l07/`、`pol2/` 的规则叠加，这些增量作用于不同的键或不同的字段，可以交换。

## 3. 自检
- 命令：`python3 -I -B m4l03/validate_policies_m4l03.py <cache_policies_contract 目录>`，只用标准库，直接运行。
- 复用 m3m4 的全部规则，anthropic_identity 的字段集换成 T2.3 的，并加入：能力字段与“空表示关闭”；错误码 official_endpoint、not_apikey；账号候选与 account_options_unavailable 的单独核对；派生样例可以重新生成。
- 生成器：`07-design/scripts/gen_policies_m4l03.py`。

## 4. 各方动作
- **G（实现，叠在 T2.2 分支上）**：按本目录、`cache_trace_contract/m4l03/`、开工包 §10.4 与 DESIGN §4.2 实现。能力位 l03 只有在 L-03 全部实现后才能置为 true（依赖 l04）；accessor 与能力位、`!behaviorStale()` 在同一处做 AND。
- **UI**：anthropic_identity 卡片在 metadata_group_ids 之后加账号多选，候选来自 options；options 为 [] 并带 account_options_unavailable 时改为手动输入 ID（v1.0.1），不加载管理端账号列表。文案写明“为空时不生效”；显示 official_endpoint、not_apikey 两个错误与 account_options_unavailable 提示。

## 5. 变更记录
- **v1.0（2026-10-25）**：首版。
- **v1.0.1（2026-10-25）**，两条 PM 裁定（PM 2026-10-25）：
  1. **client_user_id**：客户端带了任何非空的 metadata.user_id（不论能否解析）一律不覆盖，本请求不做 L-03，trace 记 `l03_skip=client_user_id`。理由是 INV-02 客户端值优先，中转不得替换 SDK 自己的终端用户标识；带任意值的客户端得不到 L-03，代价已接受。§1 的生效条件原本就按此写，DESIGN §4.2 已同步（v1.7.8.17）。trace 契约 m4l03 的字节不变。
  2. **候选未加载时的前端**：改为手动输入 ID，不加载管理端账号列表（§1）。只改了 `m4l03_list_account_options_unavailable.json` 的 `_note` 与本 README，响应字节不变；自检与 SHA256SUMS 已重算。
  - 同时写明：提示按候选类型作用；disable 方向的 PUT 跳过账号查询。这两条是对已有规则的明文化，不改任何样例。
