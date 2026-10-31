import { describe, expect, it } from "vitest";

import {
  DEFAULT_PROFIT_MIN_MARGIN_PERCENT,
  formatProfitCeiling,
  profitDecimalToPercent,
  profitMarginPercentOnEnable,
  profitMarginPercentFromGroup,
  profitPercentToDecimal,
  profitPreviewLevel,
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

  it("treats empty inputs as zero", () => {
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
