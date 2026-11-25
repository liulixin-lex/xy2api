/**
 * The first-output line's settings (GET | PUT /admin/sched-quality/ttft-rule) as the settings page edits them:
 * percentages in the inputs, ratios in the API. Pure functions, no requests.
 */
import type { SchedRuleChange, SchedTTFTRuleField, SchedTTFTRuleValues } from '@/api/admin/schedQualityTypes'

/**
 * The settings the page shows and edits. The margin over the peers is still in the API but the rules in force no
 * longer compare with peers: it is not shown, and a save sends it back as loaded.
 */
export const TTFT_RULE_FIELDS = ['confidence', 'tolerance'] as const satisfies readonly SchedTTFTRuleField[]
export type TTFTRuleShownField = (typeof TTFT_RULE_FIELDS)[number]

/**
 * Legal ranges in percent, as the backend checks them: 50 < confidence < 100, 0 < tolerance < 100. Anything else is
 * rejected (400 INVALID_TTFT_RULE), never clamped.
 */
export const TTFT_RULE_LEGAL: Record<TTFTRuleShownField, { min: number; max: number; minIncluded: boolean }> = {
  confidence: { min: 50, max: 100, minIncluded: false },
  tolerance: { min: 0, max: 100, minIncluded: false }
}

/** Whether an input (percent, as typed) is a legal value of the field. */
export function ttftRuleInputValid(field: TTFTRuleShownField, input: string | number): boolean {
  const value = String(input).trim() === '' ? NaN : Number(input)
  const { min, max, minIncluded } = TTFT_RULE_LEGAL[field]
  return Number.isFinite(value) && (minIncluded ? value >= min : value > min) && value < max
}

/** A ratio as the input shows it: 0.8 → "80". */
export const ttftRulePercent = (ratio: number) => String(+(ratio * 100).toFixed(2))

/** Outside the replayed range (bounds included): allowed, but no data supports the setting. */
export const outsideReplayed = (range: [number, number] | undefined, ratio: number) =>
  !!range && Number.isFinite(ratio) && (ratio < range[0] - 1e-9 || ratio > range[1] + 1e-9)

/**
 * Raising any value relaxes the line (accounts go to the rear less easily), lowering tightens it; some up and some
 * down is mixed, which the backend treats as a relaxation. The margin is not edited: it always goes back as loaded.
 */
export function ttftRuleChange(saved: SchedTTFTRuleValues, next: SchedTTFTRuleValues): SchedRuleChange {
  const up = TTFT_RULE_FIELDS.some((field) => next[field] > saved[field])
  const down = TTFT_RULE_FIELDS.some((field) => next[field] < saved[field])
  if (up && down) return 'mixed'
  if (up) return 'relax'
  if (down) return 'tighten'
  return 'none'
}
