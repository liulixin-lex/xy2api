// 分组利润控制表单辅助：百分比 <-> 小数换算与提交前校验。
// 后端按小数存 decimal(10,4)（0.30 = 30%），界面按百分比输入展示；
// 固定 4 位小数精度，避免 0.3 * 100 = 30.000000000000004 之类的浮点尾数回显。

export const profitPercentToDecimal = (
  value: number | string | null | undefined,
): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 100) / 10000;
};

export const profitDecimalToPercent = (
  value: number | null | undefined,
): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 1e6) / 1e4;
};

export type ProfitControlFormState = {
  platform: string;
  profit_control_enabled: boolean;
  profit_min_margin_percent: number | string | null;
};

export const isProfitControlPlatform = (platform: string): boolean =>
  ["openai", "anthropic", "gemini", "grok", "antigravity"].includes(platform);

// 安全缓冲不再在界面显示，提交时固定为 0，上限只由毛利率决定。
export const PROFIT_SAFETY_BUFFER = 0;

// 编辑回显：把已存的安全缓冲并入毛利率，保存时缓冲提交 0，实际生效的上限 D×(1−m−b) 不变。
// 旧校验保证两者之和 < 1。
export const profitMarginPercentFromGroup = (
  margin: number | null | undefined,
  buffer: number | null | undefined,
): number => profitDecimalToPercent((margin ?? 0) + (buffer ?? 0));

// 开启利润控制时的默认毛利率（%）。只在管理员手动打开开关、且当前毛利率未填或为 0 时套用，
// 不改写已保存分组的配置。
export const DEFAULT_PROFIT_MIN_MARGIN_PERCENT = 20;

export const profitMarginPercentOnEnable = (
  current: number | string | null | undefined,
): number | string => {
  const parsed = Number(current);
  return Number.isFinite(parsed) && parsed > 0
    ? (current as number | string)
    : DEFAULT_PROFIT_MIN_MARGIN_PERCENT;
};

// 毛利率框留空（v-model.number 清空得到 ""）按默认 20% 处理，与占位一致；显式填 0 仍是 0。
// 校验、提交与预览都经过这里，三处口径相同。
export const effectiveProfitMarginPercent = (
  value: number | string | null | undefined,
): number =>
  value === null || value === undefined || String(value).trim() === ""
    ? DEFAULT_PROFIT_MIN_MARGIN_PERCENT
    : Number(value);

// 提交前校验：毛利率 ∈ [0,1)（否则上限 <= 0，所有可核价账号都会被排除）。
// 返回 null 表示通过，否则返回错误信息的 i18n key（相对 admin.groups.profitControl 前缀）。
// 仅支持平台且开关开启时才校验。
//
// 上界校验必须落在 profitPercentToDecimal 的换算结果上，而不是界面百分比：
// 后端 validProfitControlRatio 校验的是小数 [0,1)，而 99.999% 会被四舍五入
// 进位成 1.0——按百分比判 `< 100` 会让前端放行、后端 400。
export const validateProfitControlFormState = (
  form: ProfitControlFormState,
): string | null => {
  if (!isProfitControlPlatform(form.platform) || !form.profit_control_enabled) {
    return null;
  }
  const marginPercent = effectiveProfitMarginPercent(form.profit_min_margin_percent);
  if (!Number.isFinite(marginPercent) || marginPercent < 0) {
    return "marginRangeError";
  }
  if (profitPercentToDecimal(marginPercent) >= 1) {
    return "marginRangeError";
  }
  return null;
};

// 最低上游倍率（倍率区间的下限）：只用于 OpenAI 分组，与利润开关无关，填了就生效；留空表示不设。
// 后端按 decimal 存，最多 4 位小数，必须 > 0。
export const isMinUpstreamRatePlatform = (platform: string): boolean =>
  platform === "openai";

export type MinUpstreamRateInput = number | string | null | undefined;

const isBlankRate = (value: MinUpstreamRateInput): boolean =>
  value === null || value === undefined || String(value).trim() === "";

// 提交前校验：留空通过；否则必须是有限数、> 0、最多 4 位小数。
// 返回 null 表示通过，否则返回错误信息的 i18n key（相对 admin.groups.profitControl 前缀）。
export const validateMinUpstreamRate = (
  value: MinUpstreamRateInput,
): string | null => {
  if (isBlankRate(value)) return null;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return "floorRangeError";
  if (Math.abs(Math.round(parsed * 1e4) / 1e4 - parsed) > 1e-9 * Math.max(1, parsed)) {
    return "floorPrecisionError";
  }
  return null;
};

// 表单值 → 下限数值；留空或无效为 null（不设）。
export const minUpstreamRateValue = (value: MinUpstreamRateInput): number | null =>
  isBlankRate(value) || validateMinUpstreamRate(value) !== null ? null : Number(value);

// 创建：只有 OpenAI 分组、填了值才提交；不填就不传。
export const minUpstreamRateForCreate = (
  platform: string,
  value: MinUpstreamRateInput,
): number | undefined => {
  if (!isMinUpstreamRatePlatform(platform)) return undefined;
  return minUpstreamRateValue(value) ?? undefined;
};

// 编辑：与 availability_fallback_group_id 同样的可空写法，提交 0 表示清除（只在原来设过、现在清空时才发）；
// 原来就没设、现在也没填时不传；非 OpenAI 分组不传。
export const minUpstreamRateForUpdate = (
  platform: string,
  value: MinUpstreamRateInput,
  saved: number | null | undefined,
): number | undefined => {
  if (!isMinUpstreamRatePlatform(platform)) return undefined;
  const next = minUpstreamRateValue(value);
  if (next !== null) return next;
  return typeof saved === "number" && saved > 0 ? 0 : undefined;
};

// 编辑回显：没设（null / 0）显示空框。
export const minUpstreamRateFormValue = (
  rate: number | null | undefined,
): number | "" => (typeof rate === "number" && rate > 0 ? rate : "");

// 利润门预览的提示等级：有可调度成员但没有一个能通过、或倍率区间为空（下限高于上限）为红字并在保存时要求确认，
// 只剩 1 个为黄字；本组没有可调度成员时与毛利率无关，显示中性提示，不要求确认。
export type ProfitPreviewLevel = "empty" | "danger" | "warning" | "normal";

export const profitPreviewLevel = (preview: {
  schedulable: number;
  passing: number;
  range_empty?: boolean;
}): ProfitPreviewLevel => {
  if (preview.range_empty) return "danger";
  if (preview.schedulable <= 0) return "empty";
  if (preview.passing <= 0) return "danger";
  if (preview.passing === 1) return "warning";
  return "normal";
};

// 利润开关关闭、只设了下限时的预览等级：可调度成员全部低于下限（固定号不计入 below_floor）为红字。
export const floorOnlyPreviewLevel = (preview: {
  schedulable: number;
  below_floor: number;
}): ProfitPreviewLevel => {
  if (preview.schedulable <= 0) return "empty";
  if (preview.below_floor >= preview.schedulable) return "danger";
  return "normal";
};

// 保存前要求确认的原因：区间为空，或没有成员能用。
export type ProfitBlockReason = "range_empty" | "none_usable";

export const profitBlockReason = (
  preview: { schedulable: number; passing: number; below_floor: number; range_empty: boolean },
  profitEnabled: boolean,
): ProfitBlockReason | null => {
  if (profitEnabled) {
    if (preview.range_empty) return "range_empty";
    return profitPreviewLevel(preview) === "danger" ? "none_usable" : null;
  }
  return floorOnlyPreviewLevel(preview) === "danger" ? "none_usable" : null;
};

// 上游倍率上限、下限最多保留 4 位小数，去掉尾随 0（0.17600 → 0.176）。
export const formatProfitCeiling = (ceiling: number): string =>
  String(Math.round(ceiling * 1e4) / 1e4);
