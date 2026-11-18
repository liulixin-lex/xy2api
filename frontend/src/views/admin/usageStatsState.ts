import { computed, shallowRef, type ComputedRef } from 'vue'

/**
 * What the admin usage page's user-wait card may show for the current filter:
 * - loading: no answer of this filter's to show yet (a request is out, or about to be);
 * - loaded: the stats on the page are this filter's latest answer;
 * - failed: this filter's latest stats request failed;
 * - blocked: this filter's list failed to load, so its stats were not asked (they run only after a list load).
 */
export type UsageStatsPhase = 'loading' | 'loaded' | 'failed' | 'blocked'

export interface UsageStatsState {
  /** The phase for the current filter; another filter's state reads as loading. */
  phase: ComputedRef<UsageStatsPhase>
  /** The user asked again (applied a filter, refreshed): anything but a shown answer turns to loading. */
  retry: () => void
  /**
   * A stats request starts for the current filter. Settle it with its own functions; only the latest one counts, and
   * `latest()` tells whether its answer may be put on the page at all.
   */
  statsStarted: () => { latest: () => boolean; loaded: () => void; failed: () => void }
  /** A list request starts for the current filter. Only the latest one's failure counts. */
  listStarted: () => { failed: () => void }
}

/**
 * The user-wait card's state machine (one state, one key). Every request takes a generation; a request settles the
 * state only while it is the latest of its kind, so a late or cancelled answer changes nothing. A shown answer stays
 * through a refresh of the same filter (a background beat, a manual refresh) until that refresh fails; leaving any other
 * state takes a new answer of this filter's. A list success writes nothing: the stats request that follows it does.
 */
export function useUsageStatsState(currentKey: () => string): UsageStatsState {
  const state = shallowRef<{ key: string | null; phase: UsageStatsPhase }>({ key: null, phase: 'loading' })
  let statsGen = 0
  let listGen = 0
  const set = (key: string, phase: UsageStatsPhase) => { state.value = { key, phase } }
  /** The current filter's answer is on the page. */
  const shown = (key: string) => state.value.key === key && state.value.phase === 'loaded'

  return {
    phase: computed(() => (state.value.key === currentKey() ? state.value.phase : 'loading')),
    retry() {
      const key = currentKey()
      if (!shown(key)) set(key, 'loading')
    },
    statsStarted() {
      const gen = ++statsGen
      const key = currentKey()
      if (!shown(key)) set(key, 'loading')
      const latest = () => gen === statsGen
      return {
        latest,
        loaded: () => { if (latest()) set(key, 'loaded') },
        failed: () => { if (latest()) set(key, 'failed') }
      }
    },
    listStarted() {
      const gen = ++listGen
      const key = currentKey()
      return {
        failed: () => { if (gen === listGen && !shown(key)) set(key, 'blocked') }
      }
    }
  }
}
