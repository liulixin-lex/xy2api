# cache_policies_contract/combo_t22 · T2.2 临时集成基线的组合派生样例（v1.0.1）

> 作者：F，2026-10-25。回应 G：T2.2 的临时集成基线包含 M2、M2b、M3、M3.1、T4.0 与 T2.2。`m4l04/` 的 list 样例中，observability 仍是包含 M2、不含 M2b 的形态，upstream_identity 也还没有 M3.1 的字段，无法做全信封比较。
> **与其他目录的关系**：依赖的父目录、`m3m4/`、`m2m2b/`、`m31/`、`m4l04/` 一个字节都没改，sha256 记在 `FROZEN.sha256`（226 个文件）。

## 1. 组合规则（机械变换，彼此可交换）
三个增量分别作用于不同的键，叠加顺序不影响结果。在 `m4l04/` 的样例（已含 T2.2 字段）上：
1. **M2b**（`m2m2b/README.md` §1）：observability 在 `persist_trace_hourly` **之前**插入 `platforms`（enum_list，默认 `["openai"]`）与 `ingress_chain_percent`（int 0..100，默认 10）。schema、defaults、stored.spec 三处一致。存储值缺 ingress_chain_percent 时补为 min(10, chain_percent)（m2m2b v1.0.1）。
2. **M3.1**（`m31/README.md` §1）：upstream_identity 在 schema 末尾追加 `max_inflight_per_key`（int 1..16，默认 2）。存储值满足 mode=uuid_fill 且 account_ids 为空时，effective.warnings 带 `l01_all_accounts_unverified`。
3. **T2.2**（`m4l04/README.md` §1）：anthropic_identity 的两个字段，源样例中已有。

- 构建能力的前提（`_fixture.build_capabilities`）沿用源样例。
- 其余内容逐字相同；结果中 observability 的字段顺序为 M1 的 5 个、platforms、ingress_chain_percent、persist_trace_hourly。
- 将来出现其他组合时，按同样的规则叠加，生成器可以复用。

## 2. 样例
- **7 个组合样例**：`combo_m4l04_list_t22.json`，以及 6 个 list 派生（`combo_m4l04_m4_*`），各带 `_composed_from` 指向 m4l04 源样例。
- **不需要组合的 5 个**：m4l04 的 validate 与 PUT 样例，以及 T2.2 的单项样例。它们不含 observability 与 upstream_identity 条目，直接用 `m4l04/` 的原样例（见 `SCOPE.json`）。
- `invalid/` 有 3 个反例：缺 platforms、缺 max_inflight_per_key、observability 的字段顺序错误。

## 3. 自检
- 命令：`python3 -I combo_t22/validate_policies_combo_t22.py <cache_policies_contract 目录>`，只用标准库，直接运行。
- 内容：
  - 依赖目录未变；
  - 组合样例可按 §1 重新生成；
  - 复用 m3m4 的自检，把 observability、upstream_identity、anthropic_identity 的字段集与默认值换成组合后的；
  - ingress_chain_percent ≤ chain_percent。
- 生成器：`07-design/scripts/gen_policies_combo_t22.py`。

## 追加记录
- **v1.0（2026-10-25）**：首次发布。
- **v1.0.1（2026-10-25）**：依赖的 `m31/` 更新到 v1.0.2（R 提出的样例修正），只刷新 `FROZEN.sha256`，组合样例与规则不变。
