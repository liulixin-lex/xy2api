import { onScopeDispose, ref, shallowRef, watch, type WatchSource } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedAccountSummary, SchedAccountsSummary } from '@/types/schedQuality'

/** Refresh period of the drawer and the group board while they are open. */
export const SCHED_QUALITY_POLL_MS = 5000

const pageHidden = () => typeof document !== 'undefined' && document.hidden

/**
 * Summary of the accounts on the current list page. The view calls refresh() wherever it refreshes the list
 * (load, page change, incremental refresh). Like the list's incremental refresh, a call is skipped while the page is
 * hidden or `paused()` is true (a dialog or the drawer is open); the next list refresh catches up.
 */
export function useSchedQualitySummary(paused: () => boolean = () => false) {
  const view = shallowRef<Omit<SchedAccountsSummary, 'accounts'> | null>(null)
  const byAccount = shallowRef(new Map<number, SchedAccountSummary>())
  const state = ref<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
  let seq = 0
  let controller: AbortController | null = null

  async function refresh(accountIds: number[], model?: string) {
    if (pageHidden() || paused()) return
    controller?.abort()
    controller = null
    const current = ++seq
    if (!accountIds.length) {
      byAccount.value = new Map()
      state.value = 'loaded'
      return
    }
    const ctrl = new AbortController()
    controller = ctrl
    state.value = 'loading'
    try {
      const { accounts, ...rest } = await schedQualityAPI.summary(accountIds, model, { signal: ctrl.signal })
      if (current !== seq) return
      view.value = rest
      byAccount.value = new Map(accounts.map((account) => [account.account_id, account]))
      state.value = 'loaded'
    } catch (error) {
      if (current !== seq) return
      state.value = 'failed'
      console.error('Failed to load quality summary:', error)
    } finally {
      if (controller === ctrl) controller = null
    }
  }

  onScopeDispose(() => {
    seq++
    controller?.abort()
  })

  return { view, byAccount, state, refresh }
}

/**
 * Loads while `key` is not null (the drawer's account, the board's group and model), then every 5 s; ticks are
 * skipped while the page is hidden or a request is still running. A new key drops the previous data, and a response
 * for an older key is discarded. Running tests show up through this refresh; call refresh() right after an action.
 */
export function useSchedQualityPoll<T>(key: WatchSource<string | number | null>, load: (signal: AbortSignal) => Promise<T>) {
  const data = shallowRef<T | null>(null)
  const error = shallowRef<unknown>(null)
  const loading = ref(false)
  let open = false
  let seq = 0
  let controller: AbortController | null = null

  async function refresh() {
    if (!open) return
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

/** A clock for cooling countdowns: it ticks every second from local time, so no request is made for them. */
export function useSchedQualityClock() {
  const now = ref(Date.now())
  useIntervalFn(() => {
    if (!pageHidden()) now.value = Date.now()
  }, 1000)
  return now
}

/** Whole seconds left until `until`, never below 0; null when there is no end time. */
export function secondsUntil(until: string | null, now: number): number | null {
  return until === null ? null : Math.max(0, Math.ceil((Date.parse(until) - now) / 1000))
}
