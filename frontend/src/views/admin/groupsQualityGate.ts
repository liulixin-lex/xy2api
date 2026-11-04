// 分组质量门槛（v3）表单辅助：策略 <-> 表单换算、提交前校验、两段保存的失败提示。
// 后端按小数存错误上限与缓存下限（0.10、0.50），界面按百分比输入；校验落在换算后的提交值上，
// 与后端边界一致：首字上限 5–60 秒整数，0.05 ≤ 错误上限 ≤ 0.50，0.50 ≤ 缓存下限 ≤ 0.70。

import type { GroupQualityGatePolicy, GroupQualityGateV3Policy } from "@/types/groupQualityGate";

export const QUALITY_GATE_DEFAULTS = {
  max_ttft_p90_seconds: 15,
  max_error_rate: 0.1,
  min_cache_keep_rate: 0.5,
} as const;

export const QUALITY_GATE_LIMITS = {
  ttftSeconds: { min: 5, max: 60 },
  errorRate: { min: 0.05, max: 0.5 },
  cacheKeepRate: { min: 0.5, max: 0.7 },
} as const;

export type QualityGateFormState = {
  enabled: boolean;
  max_ttft_p90_seconds: number | string | null;
  max_error_rate_percent: number | string | null;
  min_cache_keep_rate_percent: number | string | null;
  strict: boolean;
};

const toPercent = (rate: number) => Math.round(rate * 1e6) / 1e4;
const fromPercent = (percent: number | string | null) => Math.round(Number(percent) * 100) / 10000;

// GET 总会给出生效的 v3；分组没有条目时为关闭、取缺省值。
export const qualityGatePolicyToForm = (policy?: GroupQualityGatePolicy | null): QualityGateFormState => {
  const v3 = policy?.v3;
  return {
    enabled: v3?.enabled ?? false,
    max_ttft_p90_seconds: v3?.max_ttft_p90_seconds ?? QUALITY_GATE_DEFAULTS.max_ttft_p90_seconds,
    max_error_rate_percent: toPercent(v3?.max_error_rate ?? QUALITY_GATE_DEFAULTS.max_error_rate),
    min_cache_keep_rate_percent: toPercent(v3?.min_cache_keep_rate ?? QUALITY_GATE_DEFAULTS.min_cache_keep_rate),
    strict: v3?.strict ?? false,
  };
};

export const qualityGateFormToV3 = (form: QualityGateFormState): GroupQualityGateV3Policy => ({
  enabled: form.enabled,
  max_ttft_p90_seconds: Number(form.max_ttft_p90_seconds),
  max_error_rate: fromPercent(form.max_error_rate_percent),
  min_cache_keep_rate: fromPercent(form.min_cache_keep_rate_percent),
  strict: form.strict,
});

// 保存时只换 v3，读到的 v2 字段原样回传；回滚到 0.5.1 时它们仍按原设置生效。
export const qualityGatePolicyWithV3 = (
  stored: GroupQualityGatePolicy | undefined,
  form: QualityGateFormState,
): GroupQualityGatePolicy => ({ ...stored, v3: qualityGateFormToV3(form) });

export const qualityGateV3Equal = (a: GroupQualityGateV3Policy, b: GroupQualityGateV3Policy): boolean =>
  a.enabled === b.enabled &&
  a.max_ttft_p90_seconds === b.max_ttft_p90_seconds &&
  a.max_error_rate === b.max_error_rate &&
  a.min_cache_keep_rate === b.min_cache_keep_rate &&
  a.strict === b.strict;

export type QualityGateField = "max_ttft_p90_seconds" | "max_error_rate_percent" | "min_cache_keep_rate_percent";

const blank = (value: number | string | null) => value === null || value === "";
const within = (value: number, range: { min: number; max: number }) =>
  Number.isFinite(value) && value >= range.min && value <= range.max;

// 值为错误信息的 i18n key（相对 admin.groupQualityGate 前缀）；空对象表示通过。
// 门槛关闭时不校验：关闭只改开关，数值沿用打开对话框时读到的配置。
export const validateQualityGateForm = (
  form: QualityGateFormState,
): Partial<Record<QualityGateField, string>> => {
  if (!form.enabled) return {};
  const errors: Partial<Record<QualityGateField, string>> = {};
  const v3 = qualityGateFormToV3(form);
  if (blank(form.max_ttft_p90_seconds) || !Number.isInteger(v3.max_ttft_p90_seconds) || !within(v3.max_ttft_p90_seconds, QUALITY_GATE_LIMITS.ttftSeconds)) {
    errors.max_ttft_p90_seconds = "maxTtftRange";
  }
  if (blank(form.max_error_rate_percent) || !within(v3.max_error_rate, QUALITY_GATE_LIMITS.errorRate)) {
    errors.max_error_rate_percent = "maxErrorRateRange";
  }
  if (blank(form.min_cache_keep_rate_percent) || !within(v3.min_cache_keep_rate, QUALITY_GATE_LIMITS.cacheKeepRate)) {
    errors.min_cache_keep_rate_percent = "minCacheKeepRange";
  }
  return errors;
};

const errorStatus = (error: unknown): unknown =>
  typeof error === "object" && error !== null ? (error as { status?: unknown }).status : undefined;

// 门槛保存在分组之后进行，失败时分组已经保存。409 是版本冲突（别人先改了），重新读取版本后提示确认再存；
// 其他错误带上后端信息。返回 i18n key（相对 admin.groupQualityGate 前缀）。
export const qualityGateSaveFailureKey = (error: unknown): string =>
  errorStatus(error) === 409 ? "groupSavedGateConflict" : "groupSavedGateFailed";

const POLICY_FIELD_TO_FORM: Record<string, QualityGateField> = {
  max_ttft_p90_seconds: "max_ttft_p90_seconds",
  max_error_rate: "max_error_rate_percent",
  min_cache_keep_rate: "min_cache_keep_rate_percent",
};

// 后端 400 在 metadata.field 里给出被拒的 v3 字段（带不带 “v3.” 前缀都认），换成要标红的表单项；
// 开关等没有输入框的字段返回 undefined。
export const qualityGateRejectedField = (error: unknown): QualityGateField | undefined => {
  if (errorStatus(error) !== 400) return undefined;
  const field = (error as { metadata?: { field?: unknown } }).metadata?.field;
  return typeof field === "string" ? POLICY_FIELD_TO_FORM[field.replace(/^v3\./, "")] : undefined;
};
