import { describe, expect, it } from "vitest";

import {
  DEFAULT_PROFIT_MIN_MARGIN_PERCENT,
  effectiveProfitMarginPercent,
  floorOnlyPreviewLevel,
  formatProfitCeiling,
  isUnreadableNumberInput,
  minUpstreamRateForCreate,
  minUpstreamRateForUpdate,
  minUpstreamRateFormValue,
  profitBlockReason,
  profitDecimalToPercent,
  profitMarginPercentOnEnable,
  profitMarginPercentFromGroup,
  profitPercentToDecimal,
  profitPreviewLevel,
  profitSaveCheck,
  validateMinUpstreamRate,
  validateProfitControlFormState,
  type ProfitControlFormState,
} from "../groupsProfitControl";

const formState = (
  overrides: Partial<ProfitControlFormState> = {},
): ProfitControlFormState => ({
  platform: "openai",
  profit_control_enabled: true,
  profit_min_margin_percent: 30,
  ...overrides,
});

describe("profitPercentToDecimal", () => {
  it("converts percent input to backend decimal", () => {
    expect(profitPercentToDecimal(30)).toBe(0.3);
    expect(profitPercentToDecimal(5)).toBe(0.05);
    expect(profitPercentToDecimal(33.33)).toBe(0.3333);
    expect(profitPercentToDecimal(99.99)).toBe(0.9999);
  });

  it("rounds to four decimal places matching decimal(10,4) storage", () => {
    expect(profitPercentToDecimal(33.333)).toBe(0.3333);
    expect(profitPercentToDecimal(0.005)).toBe(0.0001);
  });

  it("treats empty, invalid and non-positive input as zero", () => {
    expect(profitPercentToDecimal("")).toBe(0);
    expect(profitPercentToDecimal(null)).toBe(0);
    expect(profitPercentToDecimal(undefined)).toBe(0);
    expect(profitPercentToDecimal("abc")).toBe(0);
    expect(profitPercentToDecimal(-5)).toBe(0);
    expect(profitPercentToDecimal(0)).toBe(0);
  });
});

describe("profitDecimalToPercent", () => {
  it("converts backend decimal to percent without float tail noise", () => {
    expect(profitDecimalToPercent(0.3)).toBe(30);
    expect(profitDecimalToPercent(0.05)).toBe(5);
    expect(profitDecimalToPercent(0.3333)).toBe(33.33);
    expect(profitDecimalToPercent(0.9999)).toBe(99.99);
  });

  it("treats missing and non-positive values as zero", () => {
    expect(profitDecimalToPercent(null)).toBe(0);
    expect(profitDecimalToPercent(undefined)).toBe(0);
    expect(profitDecimalToPercent(0)).toBe(0);
    expect(profitDecimalToPercent(-0.3)).toBe(0);
  });

  it("round-trips representative storage values", () => {
    for (const decimal of [0.05, 0.1, 0.3, 0.3333, 0.5, 0.75, 0.9999]) {
      expect(profitPercentToDecimal(profitDecimalToPercent(decimal))).toBe(
        decimal,
      );
    }
  });
});

describe("validateProfitControlFormState", () => {
  it("passes valid enabled configurations", () => {
    expect(validateProfitControlFormState(formState())).toBeNull();
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 0 }),
      ),
    ).toBeNull();
  });

  it("validates all five supported platforms and skips unsupported platforms", () => {
    expect(
      validateProfitControlFormState(
        formState({ profit_control_enabled: false, profit_min_margin_percent: 200 }),
      ),
    ).toBeNull();
    expect(
      validateProfitControlFormState(
        formState({ platform: "anthropic", profit_min_margin_percent: 200 }),
      ),
    ).toBe("marginRangeError");
    for (const platform of ["openai", "anthropic", "gemini", "grok", "antigravity"]) {
      expect(validateProfitControlFormState(formState({ platform }))).toBeNull();
    }
    expect(
      validateProfitControlFormState(
        formState({ platform: "composite", profit_min_margin_percent: 200 }),
      ),
    ).toBeNull();
  });

  it("treats empty inputs as the 20% default", () => {
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: "" }),
      ),
    ).toBeNull();
  });

  it("rejects out-of-range margin", () => {
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 100 }),
      ),
    ).toBe("marginRangeError");
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: -1 }),
      ),
    ).toBe("marginRangeError");
  });

  // 上界按换算后的小数判定：99.999% 会被 profitPercentToDecimal 四舍五入
  // 进位成 1.0，后端 validProfitControlRatio 要求严格 < 1，前端必须先拦下。
  it("rejects percents that round up to a decimal the backend rejects", () => {
    expect(profitPercentToDecimal(99.999)).toBe(1);
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 99.999 }),
      ),
    ).toBe("marginRangeError");
  });

  it("still accepts the largest percent that survives the rounding", () => {
    expect(profitPercentToDecimal(99.99)).toBe(0.9999);
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 99.99 }),
      ),
    ).toBeNull();
  });
});

describe("effectiveProfitMarginPercent", () => {
  it("submits and previews an empty margin as the 20% default, keeping an explicit 0", () => {
    for (const empty of ["", "  ", null, undefined]) {
      expect(effectiveProfitMarginPercent(empty)).toBe(20);
      expect(profitPercentToDecimal(effectiveProfitMarginPercent(empty))).toBe(0.2);
    }
    expect(effectiveProfitMarginPercent(0)).toBe(0);
    expect(effectiveProfitMarginPercent("0")).toBe(0);
    expect(profitPercentToDecimal(effectiveProfitMarginPercent(0))).toBe(0);
    expect(effectiveProfitMarginPercent(35)).toBe(35);
    expect(effectiveProfitMarginPercent("12.5")).toBe(12.5);
    expect(Number.isNaN(effectiveProfitMarginPercent("abc"))).toBe(true);
    expect(
      validateProfitControlFormState(formState({ profit_min_margin_percent: "abc" })),
    ).toBe("marginRangeError");
  });
});

describe("profitMarginPercentOnEnable", () => {
  it("fills the 20% default only when the margin is empty or zero", () => {
    expect(DEFAULT_PROFIT_MIN_MARGIN_PERCENT).toBe(20);
    for (const empty of [0, "", null, undefined, "abc", -5]) {
      expect(profitMarginPercentOnEnable(empty)).toBe(20);
    }
    expect(profitMarginPercentOnEnable(35)).toBe(35);
    expect(profitMarginPercentOnEnable("12.5")).toBe("12.5");
  });
});

describe("profitMarginPercentFromGroup", () => {
  it("folds a stored safety buffer into the margin so the effective ceiling is unchanged", () => {
    expect(profitMarginPercentFromGroup(0.2, 0.05)).toBe(25);
    expect(profitMarginPercentFromGroup(0.1, 0.2)).toBe(30);
    expect(profitMarginPercentFromGroup(0.3333, 0.1)).toBe(43.33);
    expect(profitMarginPercentFromGroup(0.2, 0)).toBe(20);
    expect(profitMarginPercentFromGroup(undefined, null)).toBe(0);
    // 回显后再按缓冲 0 提交，m' + 0 与原来的 m + b 相同。
    expect(profitPercentToDecimal(profitMarginPercentFromGroup(0.6, 0.3999))).toBe(0.9999);
  });
});

describe("profitPreviewLevel", () => {
  it("is red when schedulable members exist but none pass, yellow for exactly one, normal otherwise", () => {
    expect(profitPreviewLevel({ schedulable: 11, passing: 0 })).toBe("danger");
    expect(profitPreviewLevel({ schedulable: 11, passing: 1 })).toBe("warning");
    expect(profitPreviewLevel({ schedulable: 11, passing: 2 })).toBe("normal");
    expect(profitPreviewLevel({ schedulable: 1, passing: 1 })).toBe("warning");
  });

  it("is neutral when the group has no schedulable members", () => {
    expect(profitPreviewLevel({ schedulable: 0, passing: 0 })).toBe("empty");
  });

  it("is red when the floor is above the ceiling, even with passing members", () => {
    expect(profitPreviewLevel({ schedulable: 11, passing: 4, range_empty: true })).toBe("danger");
    expect(profitPreviewLevel({ schedulable: 0, passing: 0, range_empty: true })).toBe("danger");
  });
});

describe("validateMinUpstreamRate", () => {
  it("accepts an empty box as no floor", () => {
    for (const value of ["", "  ", null, undefined]) {
      expect(validateMinUpstreamRate(value)).toBeNull();
    }
  });

  it("accepts finite positive values with at most four decimals", () => {
    for (const value of [0.0001, 0.1234, 0.5, 1, 2.75, "0.3", 999999.9999]) {
      expect(validateMinUpstreamRate(value)).toBeNull();
    }
  });

  it("rejects zero, negatives, non-numbers and the backend's bound of 1000000 or more", () => {
    for (const value of [0, -0.1, "abc", Number.POSITIVE_INFINITY, Number.NaN, 1e6, "1e6", 1234567]) {
      expect(validateMinUpstreamRate(value)).toBe("floorRangeError");
    }
  });

  it("rejects a fifth decimal instead of rounding it, as the backend counts the decimals of the value sent", () => {
    for (const value of [0.12345, 0.00001, "1e-10", "1000.00001", 123456.12345]) {
      expect(validateMinUpstreamRate(value)).toBe("floorPrecisionError");
    }
    expect(validateMinUpstreamRate("1e-4")).toBeNull();
  });
});

// What a save checks for "no usable account", from the payload it submits and the saved group.
describe("profitSaveCheck", () => {
  const saved = { profit_control_enabled: true, profit_min_margin: 0.15, profit_safety_buffer: 0.05, min_upstream_rate: 0.3, rate_multiplier: 1 };
  // What the edit form submits for the saved group when nothing is changed (the buffer is folded into the margin).
  const same = { profit_control_enabled: true, profit_min_margin: 0.2, min_upstream_rate: 0.3, rate_multiplier: 1 };

  it("checks nothing when the switch, margin, floor and multiplier are as saved, or when nothing can block after saving", () => {
    expect(profitSaveCheck(same, saved)).toBeNull();
    // Off and no floor after saving: profit control cannot leave the group without accounts.
    expect(profitSaveCheck({ ...same, profit_control_enabled: false, min_upstream_rate: 0 }, saved)).toBeNull();
    // Margin and multiplier only count while profit control is on.
    const off = { ...saved, profit_control_enabled: false };
    expect(profitSaveCheck({ ...same, profit_control_enabled: false, profit_min_margin: 0.4, rate_multiplier: 2 }, off)).toBeNull();
  });

  it("checks the submitted values when one of them changed", () => {
    expect(profitSaveCheck({ ...same, min_upstream_rate: 2 }, saved)).toEqual({ profitEnabled: true, margin: 0.2, floor: 2, rateChanged: false });
    expect(profitSaveCheck({ ...same, profit_min_margin: 0.3 }, saved)).toEqual({ profitEnabled: true, margin: 0.3, floor: 0.3, rateChanged: false });
    // Clearing the floor on edit submits 0; a floor not sent stays as saved.
    expect(profitSaveCheck({ ...same, profit_min_margin: 0.3, min_upstream_rate: undefined }, saved)).toMatchObject({ floor: 0.3 });
    expect(profitSaveCheck({ ...same, min_upstream_rate: 0 }, saved)).toMatchObject({ floor: 0 });
    expect(profitSaveCheck({ ...same, profit_control_enabled: false }, saved)).toEqual({ profitEnabled: false, margin: 0, floor: 0.3, rateChanged: false });
  });

  it("marks a changed multiplier while profit control is on: the preview uses the saved one", () => {
    expect(profitSaveCheck({ ...same, rate_multiplier: 0.5 }, saved)).toMatchObject({ rateChanged: true });
  });

  // The ceiling is base × the peak multiplier then × (1 − margin): a peak setting that takes effect moves it too.
  it("marks a change of the peak setting that takes effect, and nothing that leaves the multiplier as it is", () => {
    const peaked = { ...saved, subscription_type: "subscription", peak_rate_enabled: true, peak_start: "09:00", peak_end: "18:00", peak_rate_multiplier: 1 };
    const unchanged = { ...same, subscription_type: "subscription", peak_rate_enabled: true, peak_start: "09:00", peak_end: "18:00", peak_rate_multiplier: 1 };
    expect(profitSaveCheck(unchanged, peaked)).toBeNull();
    for (const patch of [{ peak_rate_multiplier: 0.5 }, { peak_end: "20:00" }, { peak_rate_enabled: false }, { subscription_type: "standard" }]) {
      expect(profitSaveCheck({ ...unchanged, ...patch }, peaked)).toMatchObject({ rateChanged: true });
    }
    // Peak settings of a group they do not apply to (standard type, or peak off) leave the multiplier alone.
    const standard = { ...peaked, subscription_type: "standard" };
    expect(profitSaveCheck({ ...unchanged, subscription_type: "standard", peak_rate_multiplier: 0.5 }, standard)).toBeNull();
    const peakOff = { ...peaked, peak_rate_enabled: false };
    expect(profitSaveCheck({ ...unchanged, peak_rate_enabled: false, peak_start: "10:00" }, peakOff)).toBeNull();
  });
});

describe("min upstream rate submit values", () => {
  it("creates an OpenAI group with the floor only when one is entered", () => {
    expect(minUpstreamRateForCreate("openai", "")).toBeUndefined();
    expect(minUpstreamRateForCreate("openai", 0.3)).toBe(0.3);
    expect(minUpstreamRateForCreate("anthropic", 0.3)).toBeUndefined();
  });

  it("clears a stored floor with 0, leaves an unset one alone and never sends it for other platforms", () => {
    expect(minUpstreamRateForUpdate("openai", "", 0.3)).toBe(0);
    expect(minUpstreamRateForUpdate("openai", "", null)).toBeUndefined();
    expect(minUpstreamRateForUpdate("openai", 0.25, null)).toBe(0.25);
    expect(minUpstreamRateForUpdate("openai", 0.25, 0.3)).toBe(0.25);
    expect(minUpstreamRateForUpdate("gemini", 0.25, 0.3)).toBeUndefined();
  });

  // type=number reports "" for text it cannot read ("1e", "-"); only validity.badInput tells it from an empty box.
  it("blocks saving an unreadable floor and never clears the stored one with it; plain clearing still sends 0", () => {
    const event = (badInput: boolean) => ({ target: { validity: { badInput } } }) as unknown as Event;
    expect(isUnreadableNumberInput(event(true))).toBe(true);
    expect(isUnreadableNumberInput(event(false))).toBe(false);

    expect(validateMinUpstreamRate("", true)).toBe("floorUnreadable");
    expect(minUpstreamRateForUpdate("openai", "", 0.3, true)).toBeUndefined();
    expect(validateMinUpstreamRate("", false)).toBeNull();
    expect(minUpstreamRateForUpdate("openai", "", 0.3, false)).toBe(0);
  });

  it("echoes a stored floor and shows an empty box for none", () => {
    expect(minUpstreamRateFormValue(0.25)).toBe(0.25);
    expect(minUpstreamRateFormValue(null)).toBe("");
    expect(minUpstreamRateFormValue(undefined)).toBe("");
    expect(minUpstreamRateFormValue(0)).toBe("");
  });
});

describe("profitBlockReason", () => {
  const preview = (over: Partial<Parameters<typeof profitBlockReason>[0]> = {}) => ({
    schedulable: 6,
    passing: 3,
    below_floor: 2,
    range_empty: false,
    ...over,
  });

  it("asks for confirmation when the range is empty or nobody passes with profit control on", () => {
    expect(profitBlockReason(preview({ range_empty: true }), true)).toBe("range_empty");
    expect(profitBlockReason(preview({ passing: 0 }), true)).toBe("none_usable");
    expect(profitBlockReason(preview(), true)).toBeNull();
    expect(profitBlockReason(preview({ schedulable: 0, passing: 0 }), true)).toBeNull();
  });

  it("with profit control off judges only the floor", () => {
    // The ceiling does not apply: no passing member is not a reason by itself.
    expect(profitBlockReason(preview({ passing: 0, range_empty: true }), false)).toBeNull();
    expect(profitBlockReason(preview({ below_floor: 6 }), false)).toBe("none_usable");
    expect(floorOnlyPreviewLevel(preview({ below_floor: 6 }))).toBe("danger");
    expect(floorOnlyPreviewLevel(preview({ below_floor: 5 }))).toBe("normal");
    expect(floorOnlyPreviewLevel(preview({ schedulable: 0, below_floor: 0 }))).toBe("empty");
  });
});

describe("formatProfitCeiling", () => {
  it("keeps at most four decimals without trailing zeros", () => {
    expect(formatProfitCeiling(0.176)).toBe("0.176");
    expect(formatProfitCeiling(0.17600000001)).toBe("0.176");
    expect(formatProfitCeiling(0.123456)).toBe("0.1235");
    expect(formatProfitCeiling(1.6)).toBe("1.6");
    expect(formatProfitCeiling(0)).toBe("0");
  });
});
