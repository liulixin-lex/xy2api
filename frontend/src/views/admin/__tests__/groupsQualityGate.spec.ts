import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import { baseCompile } from "@intlify/message-compiler";
import zh from "@/i18n/locales/zh/admin/groupQualityGate";
import en from "@/i18n/locales/en/admin/groupQualityGate";

import {
  qualityGateFormToPolicy,
  qualityGatePoliciesEqual,
  qualityGatePolicyToForm,
  qualityGateSaveFailureKey,
  validateQualityGateForm,
  type QualityGateFormState,
} from "../groupsQualityGate";

const messageCompiler = (message: unknown) =>
  new Function("return " + baseCompile(String(message), { mode: "normal" }).code)();

const form = (overrides: Partial<QualityGateFormState> = {}): QualityGateFormState => ({
  enabled: true,
  max_error_rate_percent: 10,
  max_warm_ttft_p90_seconds: 20,
  fast_warm_ttft_p90_seconds: 10,
  pinned_account_ids: [],
  ...overrides,
});

describe("qualityGatePolicyToForm / qualityGateFormToPolicy", () => {
  it("shows a group without an entry as off with the defaults", () => {
    expect(qualityGatePolicyToForm(undefined)).toEqual(form({ enabled: false }));
  });

  it("converts decimals and milliseconds to percent and seconds, zero fields to defaults", () => {
    expect(
      qualityGatePolicyToForm({
        enabled: true,
        max_error_rate: 0.155,
        max_warm_ttft_p90_ms: 0,
        fast_warm_ttft_p90_ms: 7500,
        pinned_account_ids: [101],
      }),
    ).toEqual(
      form({ max_error_rate_percent: 15.5, fast_warm_ttft_p90_seconds: 7.5, pinned_account_ids: [101] }),
    );
  });

  it("round-trips to the stored units without float noise", () => {
    expect(qualityGateFormToPolicy(form({ max_error_rate_percent: 15.5, fast_warm_ttft_p90_seconds: 7.5 }))).toEqual({
      enabled: true,
      max_error_rate: 0.155,
      max_warm_ttft_p90_ms: 20000,
      fast_warm_ttft_p90_ms: 7500,
      pinned_account_ids: [],
    });
  });

  it("dedupes and sorts pinned accounts and drops ids that are no longer members", () => {
    const policy = qualityGateFormToPolicy(form({ pinned_account_ids: [9, 3, 9, 5] }), new Set([3, 9]));
    expect(policy.pinned_account_ids).toEqual([3, 9]);
    expect(qualityGateFormToPolicy(form({ pinned_account_ids: [9, 3] })).pinned_account_ids).toEqual([3, 9]);
  });

  it("treats pinned order as irrelevant when comparing for changes", () => {
    const a = qualityGateFormToPolicy(form({ pinned_account_ids: [2, 1] }));
    const b = qualityGateFormToPolicy(form({ pinned_account_ids: [1, 2] }));
    expect(qualityGatePoliciesEqual(a, b)).toBe(true);
    expect(qualityGatePoliciesEqual(a, { ...b, enabled: false })).toBe(false);
    expect(qualityGatePoliciesEqual(a, { ...b, pinned_account_ids: [1] })).toBe(false);
  });
});

describe("validateQualityGateForm", () => {
  it("passes the defaults and the boundary values", () => {
    expect(validateQualityGateForm(form())).toEqual({});
    expect(
      validateQualityGateForm(
        form({ max_error_rate_percent: 100, max_warm_ttft_p90_seconds: 600, fast_warm_ttft_p90_seconds: 1 }),
      ),
    ).toEqual({});
  });

  it("skips validation while the gate is off", () => {
    expect(validateQualityGateForm(form({ enabled: false, max_error_rate_percent: "" }))).toEqual({});
  });

  it.each([
    [0, "errorRateRange"],
    ["", "errorRateRange"],
    [100.1, "errorRateRange"],
    [-1, "errorRateRange"],
    ["abc", "errorRateRange"],
  ])("rejects error rate %s%%", (value, key) => {
    expect(validateQualityGateForm(form({ max_error_rate_percent: value })).max_error_rate_percent).toBe(key);
  });

  it("rejects a breaker line above 600 s or not above 1 s", () => {
    expect(validateQualityGateForm(form({ max_warm_ttft_p90_seconds: 600.5 })).max_warm_ttft_p90_seconds).toBe("maxTtftRange");
    expect(validateQualityGateForm(form({ max_warm_ttft_p90_seconds: 1 })).max_warm_ttft_p90_seconds).toBe("maxTtftRange");
    expect(validateQualityGateForm(form({ max_warm_ttft_p90_seconds: null })).max_warm_ttft_p90_seconds).toBe("maxTtftRange");
  });

  it("requires 1 s ≤ fast line < breaker line", () => {
    expect(validateQualityGateForm(form({ fast_warm_ttft_p90_seconds: 0.5 })).fast_warm_ttft_p90_seconds).toBe("fastTtftMin");
    expect(validateQualityGateForm(form({ fast_warm_ttft_p90_seconds: 20 })).fast_warm_ttft_p90_seconds).toBe("fastTtftBelowMax");
    expect(validateQualityGateForm(form({ fast_warm_ttft_p90_seconds: 25 })).fast_warm_ttft_p90_seconds).toBe("fastTtftBelowMax");
    // 熔断线本身无效时只报熔断线，不再拿它比较快档线。
    expect(validateQualityGateForm(form({ max_warm_ttft_p90_seconds: 700, fast_warm_ttft_p90_seconds: 650 }))).toEqual({
      max_warm_ttft_p90_seconds: "maxTtftRange",
    });
  });
});

describe("qualityGateSaveFailureKey", () => {
  const i18nFor = (locale: string) =>
    createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } }).global;

  it("asks for a refresh on a version conflict and says the group itself was saved", () => {
    const key = qualityGateSaveFailureKey({ status: 409, message: "version conflict" });
    expect(key).toBe("groupSavedGateConflict");
    const text = i18nFor("zh").t(`admin.groupQualityGate.${key}`);
    expect(text).toContain("分组已保存，门槛未保存");
    expect(text).toContain("刷新");
    expect(i18nFor("en").t(`admin.groupQualityGate.${key}`)).toContain("Refresh");
  });

  it.each([{ status: 400, message: "pinned account 7 is not a member" }, { status: 0 }, new Error("boom"), null])(
    "passes other failures through with the backend message (%s)",
    (error) => {
      const key = qualityGateSaveFailureKey(error);
      expect(key).toBe("groupSavedGateFailed");
      expect(i18nFor("zh").t(`admin.groupQualityGate.${key}`, { message: "固定账号 7 不是本组成员" })).toBe(
        "分组已保存，门槛未保存：固定账号 7 不是本组成员",
      );
    },
  );
});
