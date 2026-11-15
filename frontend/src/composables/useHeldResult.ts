/**
 * A manual probe's answer that stands in for the row's own snapshot only until that snapshot changes (spec 8.4).
 *
 * Cells compute what they show from the row; a probe (query, refresh button) may show its fresher answer, but once
 * the row brings a new snapshot (the probe usually writes one, and the live table brings it within seconds) the cell
 * shows the row again. `snapshot` returns what the answer supersedes (JSON data); `keep` holds the answer regardless
 * (for example while the cell's dialog is open).
 *
 *   const probe = useHeldResult<Result>(() => props.account.extra?.foo_snapshot)
 *   probe.hold(await query())
 *   const shown = computed(() => probe.current.value?.value ?? fromSnapshot.value)
 */
import { computed, shallowRef, watch, type ComputedRef } from 'vue'

export interface HeldResult<T> {
  /** `{ value }` while the held answer stands (the value may be null on purpose), else null. */
  current: ComputedRef<{ value: T } | null>
  hold: (value: T) => void
  clear: () => void
}

export function useHeldResult<T>(snapshot: () => unknown, options: { keep?: () => boolean } = {}): HeldResult<T> {
  const basis = () => JSON.stringify(snapshot() ?? null)
  const held = shallowRef<{ value: T; basis: string } | null>(null)

  watch(basis, () => {
    if (held.value && !options.keep?.()) held.value = null
  })

  const current = computed(() => {
    const entry = held.value
    if (!entry) return null
    return options.keep?.() || entry.basis === basis() ? { value: entry.value } : null
  })

  return {
    current,
    hold: (value) => {
      held.value = { value, basis: basis() }
    },
    clear: () => {
      held.value = null
    }
  }
}
