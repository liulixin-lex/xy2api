import type { AdminGroup, OpenAITierMode } from "@/types";

export function supportsGroupOpenAIFast(platform: string): boolean {
  return platform === "openai" || platform === "composite";
}

export interface GroupTierPolicy {
  openai_fast_mode: OpenAITierMode;
  openai_ultrafast_mode: OpenAITierMode;
  free_openai_fast: boolean;
  free_openai_ultrafast: boolean;
}

export function groupTierPolicy(group?: AdminGroup): GroupTierPolicy {
  return {
    openai_fast_mode: group?.openai_fast_mode ?? (group?.force_openai_fast ? "force_on" : "inherit"),
    openai_ultrafast_mode: group?.openai_ultrafast_mode ?? "inherit",
    free_openai_fast: group?.free_openai_fast ?? false,
    free_openai_ultrafast: group?.free_openai_ultrafast ?? false,
  };
}
