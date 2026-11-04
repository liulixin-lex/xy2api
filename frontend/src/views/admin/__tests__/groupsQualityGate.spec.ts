import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import { baseCompile } from "@intlify/message-compiler";
import zh from "@/i18n/locales/zh/admin/groupQualityGate";
import en from "@/i18n/locales/en/admin/groupQualityGate";
import type { GroupQualityGatePolicy } from "@/types/groupQualityGate";

import {
  qualityGateFormToV3,
  qualityGatePolicyToForm,
  qualityGatePolicyWithV3,
  qualityGateRejectedField,
  qualityGateSaveFailureKey,
  qualityGateV3Equal,
  validateQualityGateForm,
  type QualityGateFormState,
} from "../groupsQualityGate";

const messageCompiler = (message: unknown) =>
  new Function("return " + baseCompile(String(message), { mode: "normal" }).code)();

const form = (overrides: Partial<QualityGateFormState> = {}): QualityGateFormState => ({
  enabled: true,
  max_ttft_p90_seconds: 15,
  max_error_rate_percent: 10,
  min_cache_keep_rate_percent: 50,
  strict: false,
  ...overrides,
});

// 现网 v2 条目：v3 子对象由 GET 按继承规则填好。
const stored: GroupQualityGatePolicy = {
  enabled: true,
  max_error_rate: 0.08,
  max_warm_ttft_p90_ms: 20000,
  fast_warm_ttft_p90_ms: 10000,
  pinned_account_ids: [101],
  v3: { enabled: true, max_ttft_p90_seconds: 10, max_error_rate: 0.08, min_cache_keep_rate: 0.5, strict: true },
};

describe("qualityGatePolicyToForm / qualityGateFormToV3", () => {
  it("shows a group without an entry as off with the defaults", () => {
    expect(qualityGatePolicyToForm(undefined)).toEqual(form({ enabled: false }));
  });

  it("reads only the effective v3 and shows rates as percent", () => {
    expect(qualityGatePolicyToForm(stored)).toEqual(
      form({ max_ttft_p90_seconds: 10, max_error_rate_percent: 8, strict: true }),
    );
  });

  it("round-trips percent to the stored decimals without float noise", () => {
    expect(qualityGateFormToV3(form({ max_error_rate_percent: 7.5, min_cache_keep_rate_percent: 62.5 }))).toEqual({
      enabled: true,
      max_ttft_p90_seconds: 15,
      max_error_rate: 0.075,
      min_cache_keep_rate: 0.625,
      strict: false,
    });
  });

  it("puts the edited v3 into the policy and passes the v2 fields back untouched", () => {
    const policy = qualityGatePolicyWithV3(stored, form({ max_ttft_p90_seconds: 20 }));
    expect(policy).toEqual({
      enabled: true,
      max_error_rate: 0.08,
      max_warm_ttft_p90_ms: 20000,
      fast_warm_ttft_p90_ms: 10000,
      pinned_account_ids: [101],
      v3: { enabled: true, max_ttft_p90_seconds: 20, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false },
    });
    // 分组没有条目时只带 v3，不凭空写出 v2 字段。
    expect(Object.keys(qualityGatePolicyWithV3(undefined, form()))).toEqual(["v3"]);
  });

  it("compares every v3 field when deciding whether to save", () => {
    const a = qualityGateFormToV3(form());
    expect(qualityGateV3Equal(a, qualityGateFormToV3(form()))).toBe(true);
    expect(qualityGateV3Equal(a, { ...a, strict: true })).toBe(false);
    expect(qualityGateV3Equal(a, { ...a, enabled: false })).toBe(false);
    expect(qualityGateV3Equal(a, { ...a, min_cache_keep_rate: 0.6 })).toBe(false);
  });
});

describe("validateQualityGateForm", () => {
  it("passes the defaults and the boundary values", () => {
    expect(validateQualityGateForm(form())).toEqual({});
    expect(
      validateQualityGateForm(form({ max_ttft_p90_seconds: 5, max_error_rate_percent: 5, min_cache_keep_rate_percent: 50 })),
    ).toEqual({});
    expect(
      validateQualityGateForm(form({ max_ttft_p90_seconds: 60, max_error_rate_percent: 50, min_cache_keep_rate_percent: 70 })),
    ).toEqual({});
  });

  it("skips validation while the gate is off", () => {
    expect(validateQualityGateForm(form({ enabled: false, max_error_rate_percent: "", max_ttft_p90_seconds: 99 }))).toEqual({});
  });

  it.each([4, 61, 15.5, "", null, "abc"])("rejects a first-token limit of %s", (value) => {
    expect(validateQualityGateForm(form({ max_ttft_p90_seconds: value })).max_ttft_p90_seconds).toBe("maxTtftRange");
  });

  it.each([4.9, 50.1, 0, "", null])("rejects an error limit of %s%%", (value) => {
    expect(validateQualityGateForm(form({ max_error_rate_percent: value })).max_error_rate_percent).toBe("maxErrorRateRange");
  });

  it.each([49.9, 70.1, "", null])("rejects a cache floor of %s%%", (value) => {
    expect(validateQualityGateForm(form({ min_cache_keep_rate_percent: value })).min_cache_keep_rate_percent).toBe("minCacheKeepRange");
  });

  it("accepts fractional percentages inside the range", () => {
    expect(validateQualityGateForm(form({ max_error_rate_percent: 7.5, min_cache_keep_rate_percent: 55.5 }))).toEqual({});
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
    expect(text).toContain("再次保存");
    expect(i18nFor("en").t(`admin.groupQualityGate.${key}`)).toContain("Refresh");
  });

  it.each([{ status: 400, message: "max_error_rate out of range" }, { status: 0 }, new Error("boom"), null])(
    "passes other failures through with the backend message (%s)",
    (error) => {
      const key = qualityGateSaveFailureKey(error);
      expect(key).toBe("groupSavedGateFailed");
      expect(i18nFor("zh").t(`admin.groupQualityGate.${key}`, { message: "错误上限超出范围" })).toBe(
        "分组已保存，门槛未保存：错误上限超出范围",
      );
    },
  );
});

describe("qualityGateRejectedField", () => {
  it.each([
    ["v3.max_ttft_p90_seconds", "max_ttft_p90_seconds"],
    ["v3.max_error_rate", "max_error_rate_percent"],
    ["v3.min_cache_keep_rate", "min_cache_keep_rate_percent"],
  ])("maps a 400 on %s to the form field to highlight", (field, formField) => {
    expect(qualityGateRejectedField({ status: 400, message: "x", metadata: { field } })).toBe(formField);
  });

  it("returns nothing for fields without an input, other statuses and missing metadata", () => {
    expect(qualityGateRejectedField({ status: 400, metadata: { field: "v3.strict" } })).toBeUndefined();
    // 只认 “v3.<字段>”；裸字段名与 v2 字段不再映射。
    expect(qualityGateRejectedField({ status: 400, metadata: { field: "max_error_rate" } })).toBeUndefined();
    expect(qualityGateRejectedField({ status: 400, metadata: { field: "pinned_account_ids" } })).toBeUndefined();
    expect(qualityGateRejectedField({ status: 400 })).toBeUndefined();
    expect(qualityGateRejectedField({ status: 409, metadata: { field: "v3.max_error_rate" } })).toBeUndefined();
    expect(qualityGateRejectedField(null)).toBeUndefined();
  });
});
