/** A payment method's single-order range on the pay base (what the gateway is asked for, before fees); 0 = no limit. */
export interface PayBaseRange {
  min: number
  max: number
}

const MAX_CENTS = 1e12

function fits(payBase: number, range: PayBaseRange): boolean {
  if (range.min > 0 && payBase < range.min) return false
  if (range.max > 0 && payBase > range.max) return false
  return true
}

/**
 * The recharge amount (what the user types, whole cents) nearest to `amount` in `direction` whose pay base fits one of
 * `ranges` — the number to put in an "amount too low / too high" hint, since the limits apply to the pay base, which a
 * discount tier makes lower than the typed amount.
 *
 * `payBaseOf` is the page's own quote, so nothing is re-derived. The quote can only step down where a tier starts
 * (`tierStarts`); between two starts it never falls as the amount grows, so each stretch is binary-searched for its
 * edge. The result is then checked against the quote itself (it fits, and the cent before it in the search direction
 * does not); when that check fails or there is no payable amount that way, it returns null and the caller falls back to
 * stating the limit on the pay base.
 */
export function nearestPayableAmount(
  amount: number,
  direction: 'up' | 'down',
  ranges: PayBaseRange[],
  payBaseOf: (amount: number) => number,
  tierStarts: number[],
): number | null {
  if (!Number.isFinite(amount) || amount <= 0 || ranges.length === 0) return null
  const from = Math.round(amount * 100)
  const base = (cents: number) => payBaseOf(cents / 100)
  const payable = (cents: number) => ranges.some((range) => fits(base(cents), range))

  // Stretches [start, end] in cents where the quote is monotonic; the last one is open.
  const starts = [...new Set([1, ...tierStarts.filter((t) => Number.isFinite(t) && t > 0).map((t) => Math.ceil(t * 100 - 1e-6))])].sort((a, b) => a - b)
  const stretches = starts.map((start, i) => ({ start, end: i + 1 < starts.length ? starts[i + 1] - 1 : MAX_CENTS }))

  // First cent in [lo, hi] whose pay base reaches `target` (pay base never falls inside a stretch), or null.
  const firstReaching = (lo: number, hi: number, target: number): number | null => {
    if (base(hi) < target) return null
    while (lo < hi) {
      const mid = Math.floor((lo + hi) / 2)
      if (base(mid) >= target) hi = mid
      else lo = mid + 1
    }
    return lo
  }
  // Last cent in [lo, hi] whose pay base stays within `target`, or null.
  const lastWithin = (lo: number, hi: number, target: number): number | null => {
    if (base(lo) > target) return null
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2)
      if (base(mid) <= target) lo = mid
      else hi = mid - 1
    }
    return lo
  }

  let best: number | null = null
  for (const { start, end } of stretches) {
    const lo = direction === 'up' ? Math.max(start, from) : start
    const hi = direction === 'up' ? end : Math.min(end, from)
    if (lo > hi) continue
    for (const range of ranges) {
      let cents: number | null
      if (direction === 'up') {
        cents = range.min > 0 ? firstReaching(lo, hi, range.min) : lo
      } else {
        cents = range.max > 0 ? lastWithin(lo, hi, range.max) : hi
      }
      if (cents === null || !fits(base(cents), range)) continue
      if (best === null || (direction === 'up' ? cents < best : cents > best)) best = cents
    }
  }
  if (best === null || !payable(best)) return null
  const before = direction === 'up' ? best - 1 : best + 1
  if ((direction === 'up' ? before >= from : before <= from) && payable(before)) return null
  return best / 100
}
