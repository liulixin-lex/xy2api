import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { useUsageStatsState } from '../usageStatsState'

// The user-wait card's state machine (third review): loading, loaded, failed, blocked; only the latest request of
// each kind settles it; a shown answer stays through a refresh of the same filter until that refresh fails.
function machine(initial = 'A') {
  const key = ref(initial)
  const state = useUsageStatsState(() => key.value)
  return { state, setKey: (next: string) => { key.value = next } }
}

describe('useUsageStatsState', () => {
  it('reads loading until the current filter\'s answer, and another filter\'s state as loading', () => {
    const { state, setKey } = machine()
    expect(state.phase.value).toBe('loading')
    state.statsStarted().loaded()
    expect(state.phase.value).toBe('loaded')
    setKey('B')
    expect(state.phase.value).toBe('loading')
    setKey('A')
    expect(state.phase.value).toBe('loaded')
  })

  it('keeps a shown answer through a refresh of the same filter, until that refresh fails', () => {
    const { state } = machine()
    state.statsStarted().loaded()
    const refresh = state.statsStarted()
    expect(state.phase.value).toBe('loaded')
    state.retry()
    expect(state.phase.value).toBe('loaded')
    refresh.failed()
    expect(state.phase.value).toBe('failed')
  })

  it('shows loading for a retry from a failure, and the old answer never comes back without a new one', () => {
    const { state } = machine()
    state.statsStarted().loaded()
    state.statsStarted().failed()
    state.retry()
    expect(state.phase.value).toBe('loading')
    const retried = state.statsStarted()
    expect(state.phase.value).toBe('loading')
    retried.failed()
    expect(state.phase.value).toBe('failed')
    state.retry()
    state.listStarted().failed()
    expect(state.phase.value).toBe('blocked')
    state.statsStarted().loaded()
    expect(state.phase.value).toBe('loaded')
  })

  it('lets only the latest stats request settle the state, in either order', () => {
    const { state } = machine()
    const older = state.statsStarted()
    const newer = state.statsStarted()
    newer.failed()
    older.loaded()
    expect(state.phase.value).toBe('failed')
    expect(older.latest()).toBe(false)
    expect(newer.latest()).toBe(true)
    const third = state.statsStarted()
    const fourth = state.statsStarted()
    fourth.loaded()
    third.failed()
    expect(state.phase.value).toBe('loaded')
  })

  it('blocks on the latest list failure only, and not over a shown answer', () => {
    const { state, setKey } = machine()
    const older = state.listStarted()
    state.listStarted()
    older.failed()
    expect(state.phase.value).toBe('loading')
    state.listStarted().failed()
    expect(state.phase.value).toBe('blocked')
    state.statsStarted().loaded()
    state.listStarted().failed()
    expect(state.phase.value).toBe('loaded')
    setKey('B')
    state.listStarted().failed()
    expect(state.phase.value).toBe('blocked')
  })
})
