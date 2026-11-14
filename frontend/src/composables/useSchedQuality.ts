/** Whole seconds left until `until`, never below 0; null when there is no end time. */
export function secondsUntil(until: string | null, now: number): number | null {
  return until === null ? null : Math.max(0, Math.ceil((Date.parse(until) - now) / 1000))
}
