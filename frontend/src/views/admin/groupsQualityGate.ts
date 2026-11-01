// 分组质量门槛表单辅助：策略 <-> 表单换算、提交前校验、两段保存的失败提示。
// 后端按小数与毫秒存储（0.10、20000），界面按百分比与秒输入；校验落在换算后的提交值上，
// 与后端边界一致：0 < 错误率 ≤ 1；1000 ≤ 快档线 < 熔断线 ≤ 600000。

import type { GroupQualityGatePolicy } from "@/types/groupQualityGate";

export const QUALITY_GATE_DEFAULTS = {
  max_error_rate: 0.1,
  max_warm_ttft_p90_ms: 20000,
  fast_warm_ttft_p90_ms: 10000,
} as const;

const FAST_TTFT_MIN_MS = 1000;
const MAX_TTFT_LIMIT_MS = 600000;

export type QualityGateFormState = {
  enabled: boolean;
  max_error_rate_percent: number | string | null;
  max_warm_ttft_p90_seconds: number | string | null;
  fast_warm_ttft_p90_seconds: number | string | null;
  pinned_account_ids: number[];
};

const positiveOr = (value: number | undefined, fallback: number) =>
  value !== undefined && value > 0 ? value : fallback;

// 分组没有条目时为关闭；零值字段与后端一样取默认。
export const qualityGatePolicyToForm = (
  policy?: GroupQualityGatePolicy | null,
): QualityGateFormState => ({
  enabled: policy?.enabled ?? false,
  max_error_rate_percent:
    Math.round(
      positiveOr(policy?.max_error_rate, QUALITY_GATE_DEFAULTS.max_error_rate) * 1e6,
    ) / 1e4,
  max_warm_ttft_p90_seconds:
    positiveOr(
      policy?.max_warm_ttft_p90_ms,
      QUALITY_GATE_DEFAULTS.max_warm_ttft_p90_ms,
    ) / 1000,
  fast_warm_ttft_p90_seconds:
    positiveOr(
      policy?.fast_warm_ttft_p90_ms,
      QUALITY_GATE_DEFAULTS.fast_warm_ttft_p90_ms,
    ) / 1000,
  pinned_account_ids: [...(policy?.pinned_account_ids ?? [])],
});

// memberIds 给出时只保留本组当前成员：后端 PUT 会拒绝非成员，已移出分组的旧 ID 在这里去掉。
export const qualityGateFormToPolicy = (
  form: QualityGateFormState,
  memberIds?: ReadonlySet<number>,
): GroupQualityGatePolicy => ({
  enabled: form.enabled,
  max_error_rate: Math.round(Number(form.max_error_rate_percent) * 100) / 10000,
  max_warm_ttft_p90_ms: Math.round(Number(form.max_warm_ttft_p90_seconds) * 1000),
  fast_warm_ttft_p90_ms: Math.round(Number(form.fast_warm_ttft_p90_seconds) * 1000),
  pinned_account_ids: [...new Set(form.pinned_account_ids)]
    .filter((id) => !memberIds || memberIds.has(id))
    .sort((a, b) => a - b),
});

export const qualityGatePoliciesEqual = (
  a: GroupQualityGatePolicy,
  b: GroupQualityGatePolicy,
): boolean =>
  a.enabled === b.enabled &&
  a.max_error_rate === b.max_error_rate &&
  a.max_warm_ttft_p90_ms === b.max_warm_ttft_p90_ms &&
  a.fast_warm_ttft_p90_ms === b.fast_warm_ttft_p90_ms &&
  a.pinned_account_ids.length === b.pinned_account_ids.length &&
  a.pinned_account_ids.every((id, index) => id === b.pinned_account_ids[index]);

export type QualityGateField =
  | "max_error_rate_percent"
  | "max_warm_ttft_p90_seconds"
  | "fast_warm_ttft_p90_seconds";

// 值为错误信息的 i18n key（相对 admin.groupQualityGate 前缀）；空对象表示通过。
// 门槛关闭时不校验：关闭只改开关，数值沿用打开对话框时读到的配置。
export const validateQualityGateForm = (
  form: QualityGateFormState,
): Partial<Record<QualityGateField, string>> => {
  if (!form.enabled) return {};
  const errors: Partial<Record<QualityGateField, string>> = {};
  const policy = qualityGateFormToPolicy(form);
  const rate = policy.max_error_rate;
  if (!Number.isFinite(rate) || rate <= 0 || rate > 1) {
    errors.max_error_rate_percent = "errorRateRange";
  }
  const maxMs = policy.max_warm_ttft_p90_ms;
  const maxValid =
    Number.isFinite(maxMs) && maxMs > FAST_TTFT_MIN_MS && maxMs <= MAX_TTFT_LIMIT_MS;
  if (!maxValid) {
    errors.max_warm_ttft_p90_seconds = "maxTtftRange";
  }
  const fastMs = policy.fast_warm_ttft_p90_ms;
  if (!Number.isFinite(fastMs) || fastMs < FAST_TTFT_MIN_MS) {
    errors.fast_warm_ttft_p90_seconds = "fastTtftMin";
  } else if (maxValid && fastMs >= maxMs) {
    errors.fast_warm_ttft_p90_seconds = "fastTtftBelowMax";
  }
  return errors;
};

const errorStatus = (error: unknown): unknown =>
  typeof error === "object" && error !== null ? (error as { status?: unknown }).status : undefined;

// 门槛保存在分组之后进行，失败时分组已经保存。409 是版本冲突（别人先改了），重新读取版本后提示确认再存；
// 其他错误带上后端信息。返回 i18n key（相对 admin.groupQualityGate 前缀）。
export const qualityGateSaveFailureKey = (error: unknown): string =>
  errorStatus(error) === 409 ? "groupSavedGateConflict" : "groupSavedGateFailed";

export type QualityGateRejectedField = QualityGateField | "pinned_account_ids";

const POLICY_FIELD_TO_FORM: Record<string, QualityGateRejectedField> = {
  max_error_rate: "max_error_rate_percent",
  max_warm_ttft_p90_ms: "max_warm_ttft_p90_seconds",
  fast_warm_ttft_p90_ms: "fast_warm_ttft_p90_seconds",
  pinned_account_ids: "pinned_account_ids",
};

// 后端 400 在 metadata.field 里给出被拒的策略字段，换成要标红的表单项；开关等没有输入框的字段返回 undefined。
export const qualityGateRejectedField = (error: unknown): QualityGateRejectedField | undefined => {
  if (errorStatus(error) !== 400) return undefined;
  const field = (error as { metadata?: { field?: unknown } }).metadata?.field;
  return typeof field === "string" ? POLICY_FIELD_TO_FORM[field] : undefined;
};

type MemberChip = { id: number; name: string };

// 成员只取一页。总数大于已加载数时列表不完整：没加载到的已固定 ID 照常保留并按 #ID 显示为芯片，
// 保存时不裁剪（memberIds 为 undefined）；只有确知完整成员列表时才去掉已不在组内的 ID。
export const pinnedMemberScope = (
  members: MemberChip[],
  total: number,
  pinned: number[],
): { candidates: MemberChip[]; memberIds?: ReadonlySet<number> } => {
  const loadedIds = new Set(members.map((member) => member.id));
  if (total <= members.length) return { candidates: members, memberIds: loadedIds };
  const unloaded = [...new Set(pinned)].filter((id) => !loadedIds.has(id)).map((id) => ({ id, name: "" }));
  return { candidates: [...members, ...unloaded] };
};

// 固定账号多选：按名称或 #ID 筛选（不区分大小写），已选的排在前面，其余保持原顺序。
export const pinnedCandidates = <T extends { id: number; name: string }>(
  members: T[],
  pinned: number[],
  query: string,
): T[] => {
  const needle = query.trim().toLowerCase().replace(/^#/, "");
  const selected = new Set(pinned);
  return members
    .filter(
      (member) =>
        !needle ||
        member.name.toLowerCase().includes(needle) ||
        String(member.id).includes(needle),
    )
    .sort((a, b) => Number(selected.has(b.id)) - Number(selected.has(a.id)));
};
