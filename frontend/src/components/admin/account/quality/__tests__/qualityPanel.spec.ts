import { describe, expect, it } from 'vitest'

import type { BackendDecision, BackendDetailGroup, BackendGroupModel } from '@/api/admin/schedQualityTypes'
import { basis, cacheEvidence, errorEvidence, groupModel, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import { basisFigures, evidenceCauses, groupPlace, mergeDecisions, modelMarks, panelRecoverable, pickModelTab, placeEnd } from '../qualityPanel'
import { groupDotState } from '@/components/admin/account/table/accountGroups'

const group = (id: number, rows: BackendGroupModel[], exempt = false): BackendDetailGroup => ({
  group_id: id,
  name: `g${id}`,
  policy: { enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0, takeover_idle_seconds: 0 },
  exemption: exempt ? { group_id: 0, expires_at: null, reason: '' } : null,
  by_model: rows
})
const normal = { stop: null }

describe('quality panel place', () => {
  it('reads a group\'s place for a model in the order stopped, exempt, then the backend\'s placement', () => {
    const g = group(1, [groupModel('rear', { error: 'rear', ttfo: 'trial' }), groupModel('trial', { cache: 'trial' }), groupModel('front', { ttfo: 'unknown' })])
    expect(groupPlace({ stop: { source: 'auto', reason: 'rate_limit', until: null } }, g, 'rear')).toBe('stopped')
    expect(groupPlace(normal, group(1, [groupModel('rear', { error: 'rear' })], true), 'rear')).toBe('exempt')
    expect(groupPlace(normal, group(1, [groupModel('rear', { error: 'rear' }, { exempt: true })]), 'rear')).toBe('exempt')
    // Any line at the rear puts the account at the rear, even with another on trial; any trial → trial.
    expect(groupPlace(normal, g, 'rear')).toBe('rear')
    expect(groupPlace(normal, g, 'trial')).toBe('trial')
    expect(groupPlace(normal, g, 'front')).toBe('front')
    // No row for the model: no data, every line unknown, at the front.
    expect(groupPlace(normal, g, 'other')).toBe('front')
    // A model's tab is marked by its worst place: rear before trial; an exempt group is not at the rear.
    const detail = { ...normal, groups: [g, group(2, [groupModel('front', { cache: 'rear' })], true)] }
    expect([...modelMarks(detail)]).toEqual([['rear', 'rear'], ['trial', 'trial']])
  })

  // 7f-2: the detail has no `effective`; the panel reads the same three conditions the backend's flag stands for, so
  // the panel and the group's dot (which reads the summary's flag) say the same.
  it('is at the rear only while quality scheduling is on, fresh and the group\'s policy enabled, as the group\'s dot says', () => {
    const rear = group(1, [groupModel('a', { ttfo: 'rear' })])
    const off = { ...rear, policy: { ...rear.policy, enabled: false } }
    const summary = (effective: boolean) => ({
      group_status: { 1: effective ? ('rear' as const) : ('pass' as const) }, stop: null, exemptions: [], trial: [],
      rear: [{ group_id: 1, model: 'a', lines: ['ttfo' as const], until: '', exempt: false, effective }]
    })
    const cases = [
      { detail: { stop: null, global_enabled: true, stale: false, groups: [rear] }, effective: true },
      { detail: { stop: null, global_enabled: false, stale: false, groups: [rear] }, effective: false },
      { detail: { stop: null, global_enabled: true, stale: true, groups: [rear] }, effective: false },
      { detail: { stop: null, global_enabled: true, stale: false, groups: [off] }, effective: false }
    ]
    for (const { detail, effective } of cases) {
      const place = groupPlace(detail, detail.groups[0], 'a')
      expect(place).toBe(effective ? 'rear' : 'front')
      expect(groupDotState(summary(effective), 1)).toBe(effective ? 'back' : 'ok')
      expect(modelMarks(detail).get('a')).toBe(effective ? 'rear' : undefined)
    }
  })

  it('ends a rear place with its latest rear line and a trial with its latest trial; the other places have no end', () => {
    const row = groupModel('a', { ttfo: 'rear', error: 'rear', cache: 'trial' })
    Object.assign(row.lines.ttfo, { until: '2026-11-16T10:20:00Z' })
    Object.assign(row.lines.error, { until: '2026-11-16T10:50:00Z' })
    Object.assign(row.lines.cache, { trial_until: '2026-11-16T11:00:00Z' })
    expect(placeEnd(row, 'rear')).toBe('2026-11-16T10:50:00Z')
    expect(placeEnd(row, 'trial')).toBe('2026-11-16T11:00:00Z')
    expect(placeEnd(row, 'front')).toBeNull()
    expect(placeEnd(undefined, 'rear')).toBeNull()
  })

  it('offers 恢复 for a line at the rear or on trial, not for unknown or pass', () => {
    const detail = (row: BackendGroupModel) => ({ groups: [group(1, [row])], auto_states: [] })
    expect(panelRecoverable(detail(groupModel('a', { cache: 'rear' })))).toBe(true)
    expect(panelRecoverable(detail(groupModel('a', { ttfo: 'trial' })))).toBe(true)
    expect(panelRecoverable(detail(groupModel('a', { ttfo: 'unknown' })))).toBe(false)
  })

  it('shows the requested tab when the detail has it, else keeps the one shown, else the first', () => {
    expect(pickModelTab(['a', 'b'], 'b', 'a')).toBe('b')
    expect(pickModelTab(['a', 'b'], 'x', 'b')).toBe('b')
    expect(pickModelTab(['a', 'b'], null, 'x')).toBe('a')
    expect(pickModelTab([], 'a', 'a')).toBeNull()
  })
})

describe('decision figures', () => {
  it('reads each line\'s figure against its measure, then the units counted; what a record lacks is left out', () => {
    expect(basisFigures(basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 9, z: 0.103 }), samples_local: 11, samples_total: 30 }))).toEqual({
      measure: [{ label: 'over', params: { s: 8 }, value: '22.5%' }, { label: 'bar', value: '10.3%' }],
      samples: [{ label: 'samples', value: '30' }, { label: 'local', value: '11' }]
    })
    // No line drawn from the front peers: the peers' own share.
    expect(basisFigures(basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, z: null, prior_mu: 0.146 }) })).measure![1]).toEqual({ label: 'peers', value: '14.6%' })
    expect(basisFigures(basis('error', { error: errorEvidence({ failed: 6, padded: 80, limit: 0.025 }) })).measure).toEqual([
      { label: 'failed', value: '7.5%' }, { label: 'limit', value: '2.5%' }
    ])
    expect(basisFigures(basis('cache', { cache: cacheEvidence({ ratio: 0.781, min_ratio: 0.85 }) })).measure).toEqual([
      { label: 'hit', value: '78.1%' }, { label: 'min', value: '85%' }
    ])
    // Nothing counted, nothing to measure; an older record without counts has no sample line.
    expect(basisFigures(basis('ttfo', { ttft: ttfoEvidence({ n: 0, k: 0 }), samples_local: null, samples_total: null }))).toEqual({ measure: null, samples: [] })
    expect(evidenceCauses('ttfo', ttfoEvidence({ causes: ['mass_insufficient'] }))).toEqual(['mass_insufficient'])
    expect(evidenceCauses('error', errorEvidence())).toEqual([])
  })
})

describe('decision records', () => {
  const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
    id, at: '2026-11-16T10:25:10Z', group_id: 1, model: 'a', line: 'ttfo', action: 'enter', level: 1, until: '2026-11-16T11:05:00Z', ...patch
  })

  it('reads one moment\'s action on a model\'s line across groups as one entry, each group once and every record kept', () => {
    const merged = mergeDecisions([
      record(9, { group_id: 2 }),
      record(8, { group_id: 1, at: '2026-11-16T10:25:40Z' }),
      record(7, { group_id: 2 }),
      record(6, { model: 'b' }),
      record(5, { at: '2026-11-16T10:20:00Z' }),
      record(4, { action: 'renew' })
    ])
    expect(merged.map((entry) => [entry.items.map((item) => item.id), entry.groups])).toEqual([
      [[9, 8, 7], [2, 1]],
      [[6], [1]],
      [[5], [1]],
      [[4], [1]]
    ])
    // Another end is another entry.
    expect(mergeDecisions([record(2), record(1, { group_id: 2, until: '2026-11-16T11:25:00Z' })])).toHaveLength(2)
  })

  // Astra 2: ends within the minute shown merge, and each record keeps its own end (and so its evidence).
  it('keeps each record\'s own end when ends share the minute shown', () => {
    const merged = mergeDecisions([record(2, { until: '2026-11-16T11:05:01Z' }), record(1, { group_id: 2, until: '2026-11-16T11:05:59Z' })])
    expect(merged).toHaveLength(1)
    expect(merged[0].items.map((item) => [item.group_id, item.until])).toEqual([[1, '2026-11-16T11:05:01Z'], [2, '2026-11-16T11:05:59Z']])
  })
})
