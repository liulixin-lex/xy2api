import { onScopeDispose, ref, shallowRef, watch, type WatchSource } from 'vue'
import { useIntervalFn } from '@vueuse/core'

/** Refresh period of the drawer and the group board while they are open. */
export const SCHED_QUALITY_POLL_MS = 5000

const pageHidden = () => typeof document !== 'undefined' && document.hidden

/**
 * Loads while `key` is not null (the drawer's account, the board's group and model), then every 5 s; ticks are
 * skipped while the page is hidden or a request is still running. A new key drops the previous data, and a response
 * for an older key is discarded. Running tests show up through this refresh; call refresh() right after an action.
 * The promise of refresh() settles when the newest refresh for the current key is in: a refresh replaced by another one
 * waits for that one, so awaiting it means fresh data is on screen (a recovery holds its lock on it).
 */
export function useSchedQualityPoll<T>(key: WatchSource<string | number | null>, load: (signal: AbortSignal) => Promise<T>) {
  const data = shallowRef<T | null>(null)
  const error = shallowRef<unknown>(null)
  const loading = ref(false)
  let open = false
  let seq = 0
  let controller: AbortController | null = null
  /** The latest refresh() promise, and the key period it belongs to (stop() starts a new one). */
  let newest: Promise<void> = Promise.resolve()
  let period = 0

  function refresh(): Promise<void> {
    if (!open) return Promise.resolve()
    const at = period
    const mine: Promise<void> = fetchOnce().then(() => (at === period && newest !== mine ? newest : undefined))
    newest = mine
    return mine
  }

  async function fetchOnce() {
    controller?.abort()
    const current = ++seq
    const ctrl = new AbortController()
    controller = ctrl
    loading.value = true
    try {
      const result = await load(ctrl.signal)
      if (current !== seq) return
      data.value = result
      error.value = null
    } catch (err) {
      if (current !== seq) return
      error.value = err
    } finally {
      if (current === seq) {
        loading.value = false
        controller = null
      }
    }
  }

  const { pause, resume } = useIntervalFn(
    () => {
      if (!pageHidden() && !loading.value) void refresh()
    },
    SCHED_QUALITY_POLL_MS,
    { immediate: false }
  )

  function stop() {
    seq++
    period++
    controller?.abort()
    controller = null
    loading.value = false
    pause()
  }

  watch(
    key,
    (next) => {
      stop()
      open = next !== null
      data.value = null
      error.value = null
      if (!open) return
      void refresh()
      resume()
    },
    { immediate: true }
  )

  onScopeDispose(stop)

  return { data, error, loading, refresh }
}

/** Whole seconds left until `until`, never below 0; null when there is no end time. */
export function secondsUntil(until: string | null, now: number): number | null {
  return until === null ? null : Math.max(0, Math.ceil((Date.parse(until) - now) / 1000))
}
