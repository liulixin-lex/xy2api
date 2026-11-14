// The v0.6.4 admin UI on the backend 0.6.5 scheduling API: every mapping is checked against the backend's own contract
// fixtures (backend/internal/service/testdata/sched_quality), so a change on either side fails here first.
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiClient } from '../client'
import { backendSummary, previewGroupPolicy, summary } from '../admin/schedQuality'
import {
  toAccountsSummary,
  toBoard,
  toGroupPolicy,
  toQualityFilter,
  toRecentStats,
  toResetResult,
  withQualityFilter,
  type BackendAccountsSummary,
  type BackendGroupPolicy,
  type BackendOrder,
  type BackendResetResult
} from '../admin/schedQualityCompat'

const FIXTURES = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../backend/internal/service/testdata/sched_quality')
const fixture = <T>(name: string): T => JSON.parse(readFileSync(resolve(FIXTURES, `${name}.json`), 'utf8')) as T

afterEach(() => vi.restoreAllMocks())

describe('account summary', () => {
  it('turns each substandard group into one cooling capsule with its first failing dimension as the reason', () => {
    const out = toAccountsSummary(fixture<BackendAccountsSummary>('account_summary'))
    expect(out).toMatchObject({ at: '2026-11-11T12:00:00Z', stale: false, global_enabled: true })
    const [account] = out.accounts
    expect(account.cooling).toEqual([{ group_id: 53, model: 'gpt-6.1-sol', reason: 'speed', until: '2026-11-11T12:30:00Z', waiting_test: false, testing: false }])
    expect(account.groups).toEqual({ total: 1, normal: 0, cooling: 1, nodata: 0 })
    expect(account.model).toBe('gpt-6.1-sol')
    // The backend reports a median now: the p90 this field held is left empty, never estimated.
    expect(account.ttfo).toEqual({ p90_ms: null, timeout: false, n: 42 })
    expect(account.overrides).toEqual([])
  })

  it('keeps the group whose round ends last for several substandard models, and maps exemptions to exempt overrides', () => {
    const base = fixture<BackendAccountsSummary>('account_summary')
    const item = base.accounts[0]
    item.substandard = [
      { group_id: 54, model: 'a', dims: ['cache'], until: '2026-11-11T12:10:00Z', other_group: false, exempt: false },
      { group_id: 54, model: 'b', dims: ['error', 'ttfo'], until: '2026-11-11T12:40:00Z', other_group: false, exempt: false },
      { group_id: 53, model: 'c', dims: ['ttfo'], until: '2026-11-11T12:20:00Z', other_group: true, exempt: false }
    ]
    item.exemptions = [{ group_id: 54, expires_at: null, reason: 'x' }, { group_id: 0, expires_at: '2026-11-12T00:00:00Z', reason: '' }]
    const [account] = toAccountsSummary(base).accounts
    expect(account.cooling.map((c) => [c.group_id, c.model, c.reason])).toEqual([[53, 'c', 'speed'], [54, 'b', 'error']])
    expect(account.overrides).toEqual([
      { group_id: 0, mode: 'exempt', expires_at: '2026-11-12T00:00:00Z' },
      { group_id: 54, mode: 'exempt', expires_at: null }
    ])
  })

  it('reads an account without any sample as 暂无数据 and one with samples and no substandard group as 正常', () => {
    const base = fixture<BackendAccountsSummary>('account_summary')
    const item = base.accounts[0]
    item.substandard = []
    expect(toAccountsSummary(base).accounts[0].groups).toEqual({ total: 1, normal: 1, cooling: 0, nodata: 0 })
    Object.assign(item.ttfo, { n: 0 }); Object.assign(item.error, { n: 0 }); Object.assign(item.cache, { n: 0 })
    expect(toAccountsSummary(base).accounts[0].groups).toEqual({ total: 1, normal: 0, cooling: 0, nodata: 1 })
  })

  it('gives the recent-stats column the account numbers, with only the dimension over a line marked bad', () => {
    const out = toRecentStats(fixture<BackendAccountsSummary>('account_summary'))
    expect(out.as_of).toBe('2026-11-11T12:00:00Z')
    expect(out.items).toEqual([{
      account_id: 479,
      ttft: { p50_ms: 11600, n: 42, tier: 'bad' },
      error: { rate: 0.018, bad: 2, n: 88, tier: null },
      cache: { ratio: 0.74, n: 12, tier: null }
    }])
  })
})

describe('reset, policy and the board', () => {
  it('maps the reset kinds to cleared, preserved and failed items', () => {
    expect(toResetResult(fixture<BackendResetResult>('reset'))).toEqual({
      reset_at: '2026-11-11T12:00:00Z',
      cleared: ['quality', 'session_avoid', 'model_transient'].map((kind) => ({ key: kind, kind })),
      preserved: [{ key: 'iq_gate', kind: 'iq_gate', why: '' }],
      failed: [{ key: 'rate_limit', kind: 'rate_limit', error: '' }]
    })
  })

  it('passes the policy through with no per-class limits', () => {
    const raw = fixture<BackendGroupPolicy>('policy')
    expect(toGroupPolicy(raw)).toEqual({ ...raw, class_limits_ms: {} })
  })

  it('reads the board from the selection order: below at the back and cooling, stopped not eligible, exempt kept', () => {
    const board = toBoard(fixture<BackendOrder>('order'))
    expect(board).toMatchObject({ group_id: 53, model: 'gpt-6.1-sol', models: ['gpt-6.1-sol', 'gpt-6-astra'] })
    expect(board.accounts.map((a) => [a.account_id, a.rank, a.eligible, a.standing, a.state, a.reject])).toEqual([
      [7, 1, true, 'front', 'normal', ''],
      [5, 2, true, 'front', 'normal', ''],
      [3, 3, true, 'back', 'cooling', ''],
      [9, 4, false, 'front', 'normal', 'not_schedulable']
    ])
    expect(board.accounts[1].override?.mode).toBe('exempt')
    expect(board.accounts[2].verdicts).toEqual({ ttfo: 'fail', error: 'nodata', cache: 'fail' })
    expect(board.accounts[2].until).toBe('2026-11-11T12:30:00Z')
  })
})

describe('account list filter', () => {
  it('sends cooling as substandard, drops a stale demote and passes the other words on', () => {
    expect(['cooling', 'demote', 'normal', 'exempt', 'observing', '', undefined].map(toQualityFilter))
      .toEqual(['substandard', '', 'normal', 'exempt', 'observing', '', undefined])
    expect(withQualityFilter({ platform: 'openai' })).toEqual({ platform: 'openai' })
    expect(withQualityFilter(undefined)).toBeUndefined()
  })
})

describe('request sharing and preview', () => {
  it('shares one summary request between concurrent callers for the same accounts, and asks again once it settled', async () => {
    let answer: (value: unknown) => void = () => {}
    const post = vi.spyOn(apiClient, 'post').mockImplementation(() => new Promise((resolve) => { answer = resolve }))
    const a = summary([3, 1, 2])
    const b = backendSummary([1, 2, 3])
    expect(post).toHaveBeenCalledTimes(1)
    answer({ data: fixture('account_summary') })
    expect((await a).accounts[0].cooling).toHaveLength(1)
    expect((await b).accounts[0].ttfo.p50_ms).toBe(11600)

    void backendSummary([1, 2, 3])
    expect(post).toHaveBeenCalledTimes(2)
  })

  it('rejects an aborted caller at once and aborts the shared request only when no caller is left', async () => {
    const signals: AbortSignal[] = []
    let answer: (value: unknown) => void = () => {}
    vi.spyOn(apiClient, 'post').mockImplementation((_url, _body, config) => {
      signals.push((config as { signal: AbortSignal }).signal)
      return new Promise((resolve) => { answer = resolve })
    })
    const one = new AbortController()
    const two = new AbortController()
    const first = backendSummary([5], { signal: one.signal })
    const second = backendSummary([5], { signal: two.signal })
    one.abort()
    await expect(first).rejects.toMatchObject({ name: 'AbortError' })
    expect(signals[0].aborted).toBe(false)
    answer({ data: { at: 'x', stale: false, global_enabled: true, accounts: [] } })
    await expect(second).resolves.toMatchObject({ at: 'x' })

    const third = new AbortController()
    const last = backendSummary([6], { signal: third.signal })
    third.abort()
    await expect(last).rejects.toMatchObject({ name: 'AbortError' })
    expect(signals[1].aborted).toBe(true)
  })

  it('posts the unsaved policy to the preview route and always returns a list of changed accounts', async () => {
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { now: { normal: 2, below: 0 }, after: { normal: 1, below: 1 }, changed: null } })
    const update = { expected_version: 63, policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 as const } }
    expect(await previewGroupPolicy(53, update)).toEqual({ now: { normal: 2, below: 0 }, after: { normal: 1, below: 1 }, changed: [] })
    expect(post).toHaveBeenCalledWith('/admin/sched-quality/groups/53/policy/preview', update, { signal: undefined })
  })
})
