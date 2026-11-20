import { describe, expect, it } from "vitest";
import { groupTierPolicy, supportsGroupOpenAIFast } from "../groupsOpenAIFast";

describe("group service tier policies", () => {
  it("applies to OpenAI and composite groups", () => {
    expect(supportsGroupOpenAIFast("openai")).toBe(true);
    expect(supportsGroupOpenAIFast("composite")).toBe(true);
    expect(supportsGroupOpenAIFast("anthropic")).toBe(false);
  });

  it("defaults both tiers to inherit with no waivers", () => {
    expect(groupTierPolicy()).toEqual({
      openai_fast_mode: "inherit", openai_ultrafast_mode: "inherit",
      free_openai_fast: false, free_openai_ultrafast: false,
    });
  });
});
