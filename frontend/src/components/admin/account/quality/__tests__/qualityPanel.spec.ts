import { describe, expect, it } from 'vitest'

import type { BackendDetailGroup, BackendGroupModel } from '@/api/admin/schedQualityTypes'
import { cacheEvidence, errorEvidence, groupModel, line, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import { backModels, evidenceCauses, groupPlace, lineEvidence, lineIdentity, lineSecondsLeft, panelRecoverable, pickModelTab } from '../qualityPanel'
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
    expect(['ttfo', 'error', 'cache'].map((dim) => lineIdentity(undefined, dim as 'ttfo'))).toEqual(['unknown', 'unknown', 'unknown'])
    expect(['ttfo', 'error', 'cache'].map((dim) => lineIdentity(g.by_model[0], dim as 'ttfo'))).toEqual(['trial', 'rear', 'pass'])
    // Only a model with a group at the rear gets the tab mark; an exempt group is not at the rear.
    const detail = { ...normal, groups: [g, group(2, [groupModel('front', { cache: 'rear' })], true)] }
    expect([...backModels(detail)]).toEqual(['rear'])
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
      expect(backModels(detail).has('a')).toBe(effective)
    }
  })

  it('counts down a rear line to its end and a trial to the trial\'s end; the other identities have no time', () => {
    const now = Date.parse('2026-11-16T10:00:00Z')
    expect(lineSecondsLeft(line('ttfo', 'rear', { until: '2026-11-16T10:20:00Z', trial_until: '2026-11-16T11:00:00Z' }), now)).toBe(1200)
    expect(lineSecondsLeft(line('ttfo', 'trial', { until: '2026-11-16T10:20:00Z', trial_until: '2026-11-16T10:45:00Z' }), now)).toBe(2700)
    expect(lineSecondsLeft(line('ttfo', 'pass', { until: '2026-11-16T10:20:00Z' }), now)).toBeNull()
    expect(lineSecondsLeft(line('ttfo', 'unknown'), now)).toBeNull()
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

describe('line evidence', () => {
  it('lists the first-output evidence with discounted amounts, capped sessions, percentages and no peer numbers as "—"', () => {
    const items = lineEvidence('ttfo', ttfoEvidence({ n: 19.96, k: 2, s: 5, d: 2, p_abs: 0.8, p_rel: null, u: null, z: null }))
    expect(Object.fromEntries(items.map((item) => [item.key, item.value]))).toEqual({
      limit_s: '10', n: '20.0', k: '2.0', x: '0.5', s: '≥3', d: '≥2', prior_mu: '8.0%', peer_nu: '9.0%',
      p_abs: '80.0%', p_rel: '—', u: '—', z: '—'
    })
    expect(lineEvidence('ttfo', ttfoEvidence({ s: 2, d: 1 })).filter((item) => item.key === 's' || item.key === 'd').map((item) => item.value)).toEqual(['2', '1'])
    expect(evidenceCauses('ttfo', ttfoEvidence({ causes: ['mass_insufficient', 'no_peers'] }))).toEqual(['mass_insufficient', 'no_peers'])
  })

  it('lists the error and cache evidence in their units; only the first-output line has unknown causes', () => {
    expect(lineEvidence('error', errorEvidence()).map((item) => `${item.key}=${item.value}`)).toEqual([
      'limit=5.0%', 'clear=80', 'failed=1', 'unknown=2', 'padded=82', 'lower_fail=0.2%', 'lower_pass=1.2%'
    ])
    expect(lineEvidence('cache', cacheEvidence()).map((item) => `${item.key}=${item.value}`)).toEqual([
      'min_ratio=80.0%', 'turns=30', 'ratio=91.0%', 'sessions=6', 'low_sessions=0'
    ])
    expect(evidenceCauses('error', errorEvidence())).toEqual([])
    expect(evidenceCauses('ttfo', undefined)).toEqual([])
  })
})
