/**
 * Compact numbers for dense cells (spec 7a, 7b): three significant digits with K / M / B / T, rounded half up, and
 * rounded before the unit is chosen (999,950 → 1.00M, never 1000K; $9,995.00 → $10.0K). Each formatter has an exact
 * twin for the hover tip.
 *
 * - Money: below $1,000 two decimals ($999.99); from $1,000 $1.23K, $12.3K, $123K, $1.23M …
 * - Requests: below 100,000 the whole number with thousands separators (99,999); from 100,000 123K, 1.23M …
 * - Tokens: the same three-digit rule as everything else (533M, 96.1M, 1.20M); below 1,000 the whole number.
 *
 * Rounding works on integers (cents for money), so a binary fraction such as 9.995 cannot round the wrong way.
 */
const UNITS = ['K', 'M', 'B', 'T'] as const

/** a / b for non-negative integers, rounded down, exactly. */
const intDiv = (a: number, b: number) => (a - (a % b)) / b

/**
 * `n` minor units (an integer ≥ `perK`) as three significant digits of K, M, B or T, where `perK` minor units make
 * one K. The value is rounded first and the unit picked from the rounded value.
 */
function abbreviate(n: number, perK: number): string {
  let base = perK
  for (let unit = 0; unit < UNITS.length; unit++, base *= 1000) {
    const whole = intDiv(n, base)
    let exponent = String(whole).length - 1 // digits before the point, minus one
    if (exponent > 2 && unit < UNITS.length - 1) continue
    exponent = Math.min(exponent, 2)
    const step = base / 10 ** (2 - exponent) // one unit in the last kept digit; an integer (perK ≥ 1000)
    let digits = intDiv(n + step / 2, step) // three digits, rounded half up
    if (digits >= 1000 && exponent < 2) {
      exponent += 1
      digits = 100
    } else if (digits >= 1000 && unit < UNITS.length - 1) {
      return `1.00${UNITS[unit + 1]}`
    }
    return `${(digits / 10 ** (2 - exponent)).toFixed(2 - exponent)}${UNITS[unit]}`
  }
  return String(n)
}

const finite = (value: number | null | undefined) => (typeof value === 'number' && Number.isFinite(value) ? value : 0)

/** $999.99, $1.23K, $12.3K, $123K, $1.23M. */
export function formatCompactMoney(value: number | null | undefined): string {
  const v = finite(value)
  if (v < 0) return `-${formatCompactMoney(-v)}`
  const cents = Math.round(v * 100)
  if (cents < 100_000) return `$${(cents / 100).toFixed(2)}`
  return `$${abbreviate(cents, 100_000)}`
}

/** 99,999 below 100,000; 123K, 1.23M from there. */
export function formatCompactRequests(value: number | null | undefined): string {
  const n = Math.round(finite(value))
  if (n < 0) return `-${formatCompactRequests(-n)}`
  if (n < 100_000) return n.toLocaleString('en-US')
  return abbreviate(n, 1000)
}

/** 532 below 1,000; 12.3K, 533M, 1.20M from there. */
export function formatCompactTokens(value: number | null | undefined): string {
  const n = Math.round(finite(value))
  if (n < 0) return `-${formatCompactTokens(-n)}`
  if (n < 1000) return String(n)
  return abbreviate(n, 1000)
}

/** The exact amount for the hover tip: $1,234,567.89. */
export function formatExactMoney(value: number | null | undefined): string {
  const v = finite(value)
  const text = Math.abs(v).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return `${v < 0 ? '-' : ''}$${text}`
}

/** The exact count for the hover tip: 1,234,567. */
export function formatExactCount(value: number | null | undefined): string {
  return Math.round(finite(value)).toLocaleString('en-US')
}
