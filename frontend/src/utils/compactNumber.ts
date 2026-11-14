/**
 * Compact numbers for dense cells (spec 7a, 7b): three significant digits with K / M / B / T, rounded half up, and
 * rounded before the unit is chosen (999,950 → 1.00M, never 1000K; $9,995.00 → $10.0K). Each formatter has an exact
 * twin for the hover tip.
 *
 * - Money: below $1,000 two decimals ($999.99); from $1,000 $1.23K, $12.3K, $123K, $1.23M …
 * - Requests: below 100,000 the whole number with thousands separators (99,999); from 100,000 123K, 1.23M …
 * - Tokens: the same three-digit rule as everything else (533M, 96.1M, 1.20M); below 1,000 the whole number.
 *
 * Rounding works on integers (cents for money), taken from the decimal value as written: 9.995 is 9.99499… in binary,
 * and still rounds to $10.00 (1.005 → $1.01). A value that rounds to zero carries no sign (-$0.00 is never written).
 * No value (null, undefined, NaN, ±Infinity) reads "—", never 0: before the data is in, nothing is known.
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

export const NO_VALUE = '—'

const known = (value: number | null | undefined): value is number => typeof value === 'number' && Number.isFinite(value)

/**
 * |value| × scale rounded half up to an integer, on the decimal value: the product is cut to 15 significant digits
 * first, which drops the binary error (9.995 × 100 = 999.4999… → 999.5 → 1000).
 */
const roundScaled = (value: number, scale: number) => Math.round(Number((Math.abs(value) * scale).toPrecision(15)))

/** The sign of a rounded amount: none when it rounded to zero. */
const signOf = (value: number, rounded: number) => (value < 0 && rounded > 0 ? '-' : '')

/** $999.99, $1.23K, $12.3K, $123K, $1.23M. */
export function formatCompactMoney(value: number | null | undefined): string {
  if (!known(value)) return NO_VALUE
  const cents = roundScaled(value, 100)
  const sign = signOf(value, cents)
  if (cents < 100_000) return `${sign}$${(cents / 100).toFixed(2)}`
  return `${sign}$${abbreviate(cents, 100_000)}`
}

/** 99,999 below 100,000; 123K, 1.23M from there. */
export function formatCompactRequests(value: number | null | undefined): string {
  if (!known(value)) return NO_VALUE
  const n = roundScaled(value, 1)
  const sign = signOf(value, n)
  if (n < 100_000) return sign + n.toLocaleString('en-US')
  return sign + abbreviate(n, 1000)
}

/** 532 below 1,000; 12.3K, 533M, 1.20M from there. */
export function formatCompactTokens(value: number | null | undefined): string {
  if (!known(value)) return NO_VALUE
  const n = roundScaled(value, 1)
  const sign = signOf(value, n)
  if (n < 1000) return sign + String(n)
  return sign + abbreviate(n, 1000)
}

/** The exact amount for the hover tip: $1,234,567.89 (rounded like the compact one). */
export function formatExactMoney(value: number | null | undefined): string {
  if (!known(value)) return NO_VALUE
  const cents = roundScaled(value, 100)
  const text = (cents / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return `${signOf(value, cents)}$${text}`
}

/** The exact count for the hover tip: 1,234,567. */
export function formatExactCount(value: number | null | undefined): string {
  if (!known(value)) return NO_VALUE
  const n = roundScaled(value, 1)
  return signOf(value, n) + n.toLocaleString('en-US')
}
