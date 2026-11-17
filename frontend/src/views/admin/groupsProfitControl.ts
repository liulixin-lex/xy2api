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
// 后端按 decimal 存，最多 4 位小数，必须 > 0 且 < 1000000。
export const isMinUpstreamRatePlatform = (platform: string): boolean =>
  platform === "openai";

export type MinUpstreamRateInput = number | string | null | undefined;

/** The backend's bound: a floor must stay below it. */
export const MIN_UPSTREAM_RATE_LIMIT = 1_000_000;

const isBlankRate = (value: MinUpstreamRateInput): boolean =>
  value === null || value === undefined || String(value).trim() === "";

// type=number 的输入框里填了读不出数的内容（如 "1e"、"-"）时，value 是 ""，看起来和留空一样；
// 只能从 validity.badInput 区分。输入事件里记下它，校验和提交都要带上，否则编辑时会把已设的下限当成清除。
export const isUnreadableNumberInput = (event: Event): boolean =>
  (event.target as HTMLInputElement | null)?.validity?.badInput === true;

// 提交前校验：留空通过；读不出数不通过；否则与后端 ValidateMinUpstreamRate 等价：有限数、> 0、< 1000000、
// 不小于 0.0001，且实际发送的数按十进制写出最多 4 位小数（0 只由编辑时清空产生，见 minUpstreamRateForUpdate）。
// 返回 null 表示通过，否则返回错误信息的 i18n key（相对 admin.groups.profitControl 前缀）。
export const validateMinUpstreamRate = (
  value: MinUpstreamRateInput,
  unreadable = false,
): string | null => {
  if (unreadable) return "floorUnreadable";
  if (isBlankRate(value)) return null;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0 || parsed >= MIN_UPSTREAM_RATE_LIMIT) return "floorRangeError";
  if (parsed < 0.0001) return "floorPrecisionError";
  // In [0.0001, 1000000) String() writes the shortest round-trip digits without an exponent, as the backend's
  // FormatFloat(v, 'f', -1, 64) does: count the decimals of what is sent.
  const written = String(parsed);
  const dot = written.indexOf(".");
  return dot >= 0 && written.length - dot - 1 > 4 ? "floorPrecisionError" : null;
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
// 原来就没设、现在也没填时不传；非 OpenAI 分组不传；输入读不出数时不传（保留已设的下限）。
export const minUpstreamRateForUpdate = (
  platform: string,
  value: MinUpstreamRateInput,
  saved: number | null | undefined,
  unreadable = false,
): number | undefined => {
  if (!isMinUpstreamRatePlatform(platform) || unreadable) return undefined;
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

/**
 * 保存前确认的原因：预览显示的两种，加上拿不到能代表这次提交的预览——预览失败、超时或被取消（unverified），
 * 或同时改了分组倍率（预览接口按已保存的倍率计算，rate_changed）。
 */
export type ProfitSaveRisk = ProfitBlockReason | "unverified" | "rate_changed";

/** 这次提交要按哪组参数检查“会不会无号可用”：利润开关、毛利率（小数）、下限（0 = 不设）、实际倍率是否也改了。 */
export type ProfitSaveCheck = { profitEnabled: boolean; margin: number; floor: number; rateChanged: boolean };

type GroupRateFields = {
  rate_multiplier?: number | string;
  subscription_type?: string;
  peak_rate_enabled?: boolean;
  peak_start?: string;
  peak_end?: string;
  peak_rate_multiplier?: number | string;
};

/**
 * 决定分组实际倍率的配置（后端上限 = 基础倍率 × 当时的高峰倍率 × (1 − 毛利率 − 缓冲)）：基础倍率，以及高峰——
 * 只对订阅类型且开着高峰的分组生效（Group.PeakMultiplierAt），这时看起止时间和高峰倍率。下限只与账号的上游倍率
 * 比较，不受分组字段影响。两份配置的签名相同，实际倍率就相同。
 */
export const groupRateSignature = (group: GroupRateFields): string => {
  const peak =
    group.subscription_type === "subscription" && group.peak_rate_enabled === true
      ? `${group.peak_start ?? ""}-${group.peak_end ?? ""}x${Number(group.peak_rate_multiplier)}`
      : "none";
  return `${Number(group.rate_multiplier)}|${peak}`;
};

/**
 * 从这次提交的参数（与 PUT 的是同一份）和已保存的分组算出要检查什么；利润开关、毛利率、下限、倍率都没动，
 * 或保存后既不开利润控制也不设下限（不可能因此无号可用）时为 null，不检查、不发预览请求。
 * 毛利率只在开着利润控制时有意义，倍率只影响上限，同样只在开着时算改动。
 */
export const profitSaveCheck = (
  payload: { profit_control_enabled?: boolean; profit_min_margin?: number; min_upstream_rate?: number } & GroupRateFields,
  saved: {
    profit_control_enabled?: boolean;
    profit_min_margin?: number | null;
    profit_safety_buffer?: number | null;
    min_upstream_rate?: number | null;
  } & GroupRateFields,
): ProfitSaveCheck | null => {
  const profitEnabled = payload.profit_control_enabled === true;
  const margin = profitEnabled ? (payload.profit_min_margin ?? 0) : 0;
  const savedFloor = typeof saved.min_upstream_rate === "number" && saved.min_upstream_rate > 0 ? saved.min_upstream_rate : 0;
  // 不传下限表示不改（或不是 OpenAI 分组）。
  const floor = payload.min_upstream_rate === undefined ? savedFloor : payload.min_upstream_rate;
  // 回显时缓冲并入毛利率，没改时提交的就是这个数。
  const savedMargin = profitPercentToDecimal(profitMarginPercentFromGroup(saved.profit_min_margin, saved.profit_safety_buffer));
  // 基础倍率或生效的高峰配置变了：预览接口按已保存的算，不能代表提交后的上限。没提交的字段按已保存的算。
  const rateChanged = groupRateSignature({ ...saved, ...payload }) !== groupRateSignature(saved);
  const changed =
    profitEnabled !== (saved.profit_control_enabled === true) ||
    floor !== savedFloor ||
    (profitEnabled && (margin !== savedMargin || rateChanged));
  if (!changed || (!profitEnabled && floor === 0)) return null;
  return { profitEnabled, margin, floor, rateChanged: profitEnabled && rateChanged };
};

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
