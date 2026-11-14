import { describe, expect, it } from 'vitest'

import type { BackendDetailGroup, BackendGroupModel } from '@/api/admin/schedQualityTypes'
import { backModels, dimMark, groupModelState, pickModelTab } from '../qualityPanel'

const row = (model: string, failing: boolean): BackendGroupModel => {
  const dim = (verdict: string) => ({ scope: 'own', window_min: 60, n: 10, lcb: null, verdict, bad: 0 })
  return {
    model, failing, dims: [], level: 0, since: null, until: null, other_group_ids: [], exempt: false,
    ttfo: dim(failing ? 'fail' : 'pass'), error: dim('short'), cache: { ...dim('unknown'), ratio: null }
  }
}
const group = (id: number, rows: BackendGroupModel[], exempt = false): BackendDetailGroup => ({
  group_id: id,
  name: `g${id}`,
  policy: { enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0, takeover_idle_seconds: 0 },
  exemption: exempt ? { group_id: 0, expires_at: null, reason: '' } : null,
  by_model: rows
})

describe('quality panel state', () => {
  it('reads a group\'s state for a model in the order stopped, exempt, back, observing, normal', () => {
    const back = group(1, [row('a', true), row('b', false)])
    const normal = { stop: null, status: 'normal' }
    expect(groupModelState({ ...normal, stop: { source: 'auto', reason: 'rate_limit', until: null } }, back, 'a')).toBe('stopped')
    expect(groupModelState(normal, group(1, [row('a', true)], true), 'a')).toBe('exempt')
    expect(groupModelState(normal, back, 'a')).toBe('back')
    expect(groupModelState(normal, back, 'b')).toBe('ok')
    expect(groupModelState({ ...normal, status: 'observing' }, back, 'b')).toBe('observe')
    // No row for the model in this group: no samples yet.
    expect(groupModelState(normal, back, 'c')).toBe('observe')
    // The marks are the backend's verdicts; anything else reads as too few samples.
    expect(['ttfo', 'error', 'cache'].map((dim) => dimMark(back.by_model[0], dim as 'ttfo'))).toEqual(['fail', 'short', 'short'])
    // Only a model with a group at the back gets the tab mark; an exempt group is not at the back.
    const detail = { ...normal, groups: [back, group(2, [row('b', true)], true)] }
    expect([...backModels(detail)]).toEqual(['a'])
  })

  it('shows the requested tab when the detail has it, else keeps the one shown, else the first', () => {
    expect(pickModelTab(['a', 'b'], 'b', 'a')).toBe('b')
    expect(pickModelTab(['a', 'b'], 'x', 'b')).toBe('b')
    expect(pickModelTab(['a', 'b'], null, 'x')).toBe('a')
    expect(pickModelTab([], 'a', 'a')).toBeNull()
  })
})
