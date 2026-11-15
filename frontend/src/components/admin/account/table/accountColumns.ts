/**
 * The accounts table's column widths (prototype, spec 12.5): each column has a fixed width in two sizes, [compact,
 * roomy]; the name column takes what is left, between NAME_MIN and NAME_MAX.
 *
 * Compact, the ten default columns take 1086px and the table at least 1262px with the name column. At 1440 with the
 * navigation folded the table's scroll container is 1302px wide, 1287px once a classic scrollbar (15px on Windows)
 * takes its reserved gutter, so nothing scrolls sideways (7i-2, 7m-1); at 1366 the table scrolls, the name and actions
 * frozen. Roomy, they take 1206px: from 1438px (1206 + NAME_MAX) the table switches to the roomy widths, which 1920
 * reaches with the navigation open (1583px with the gutter) or folded.
 *
 * - Capacity is 155px compact and 154px roomy, room for "label $999.99 / $999" (129px of chip, 24px of padding; 7i-1,
 *   7l-11, 7q-9).
 * - The two rate columns are 88px compact and 96px roomy, with 8px of padding on the left and 6px on the right: the
 *   74px left take "9.9999x" (51.1px at 13px) with the 3px gap and the 18px refresh button (72.1px), or the account
 *   rate's sync mark (12px, 4px gap); longer values end in "…" and the tip has them whole.
 * - The actions column keeps two 28px buttons 4px apart; today's stats keep "用户扣费 $999.99" (104.6px of 106px);
 *   status "23h 59m 后恢复" (90.2px of 92px); the IQ check "Astra" and "检测中" with its spinner (123.4px of 125px).
 *
 * In either size, once the name column has reached NAME_MAX the room left over is shared evenly between the content
 * columns only: the check box, the scheduling switch, the two rate columns and the actions keep their width. The name
 * column (auto in the table) takes what the others leave, so it stays between NAME_MIN and NAME_MAX at every width,
 * also between the compact minimum and the switch to the roomy widths (12.12-1).
 */
export const COLUMN_WIDTHS: Record<string, [number, number]> = {
  select: [36, 40], status: [128, 140], schedulable: [52, 60], groups: [176, 176], sq_recent: [96, 120], iq_check: [149, 164],
  capacity: [155, 154], upstream_billing_rate: [88, 96], today_stats: [130, 152], actions: [76, 104], id: [80, 88],
  platform_type: [168, 180], priority: [112, 120], usage: [220, 240], proxy: [160, 176], rate_multiplier: [88, 96],
  upstream_balance: [140, 152], last_used_at: [120, 132], created_at: [150, 160], expires_at: [150, 160], notes: [180, 200]
}
export const NAME_MIN = 176
export const NAME_MAX = 232
/** Columns that keep their own width on a wide table (12.5). */
const FIXED = new Set(['select', 'schedulable', 'upstream_billing_rate', 'rate_multiplier', 'actions'])
const DEFAULT_WIDTH = 120

export interface AccountColumnLayout {
  roomy: boolean
  /** The table's minimum width: the columns' widths and NAME_MIN; narrower containers scroll sideways. */
  min: number
  /** Each column's width (the name column's excluded), its share of the room left over included. */
  widths: Record<string, number>
}

/** The widths of the columns `keys` (the name column left out) in a scroll container `tableWidth` wide. */
export function accountColumnLayout(keys: string[], tableWidth: number): AccountColumnLayout {
  const width = (key: string, size: 0 | 1) => COLUMN_WIDTHS[key]?.[size] ?? DEFAULT_WIDTH
  const sum = (size: 0 | 1) => keys.reduce((total, key) => total + width(key, size), 0)
  const roomy = tableWidth >= sum(1) + NAME_MAX
  const size = roomy ? 1 : 0
  const sharers = keys.filter((key) => !FIXED.has(key)).length
  // Exactly NAME_MAX left for the name column: the table has no side borders, and the auto column takes any rounding.
  const share = sharers ? Math.max(0, (tableWidth - sum(size) - NAME_MAX) / sharers) : 0
  return {
    roomy,
    min: sum(size) + NAME_MIN,
    widths: Object.fromEntries(keys.map((key) => [key, width(key, size) + (FIXED.has(key) ? 0 : share)]))
  }
}
