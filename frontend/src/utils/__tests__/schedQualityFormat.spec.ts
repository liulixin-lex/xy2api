import { describe, expect, it } from 'vitest'

import type { SchedGroupModelDetail } from '@/types/schedQuality'
import { dimDetail, fmtAt, markClass, policyParts, statusText } from '../schedQualityFormat'

const t = (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
const te = (key: string) => key.endsWith('.rate_limit')
const local = (day: number, h: number, m: number) => new Date(2026, 10, day, h, m).getTime()

describe('fmtAt', () => {
  it('shows HH:mm on the same day and MM-DD HH:mm on another', () => {
    const now = local(13, 21, 0)
    expect(fmtAt(new Date(local(13, 9, 5)).toISOString(), now)).toBe('09:05')
    expect(fmtAt(new Date(local(14, 0, 30)).toISOString(), now)).toBe('11-14 00:30')
  })
})

describe('markClass (FINAL §6)', () => {
  it('is red when the number makes the account substandard, grey for a short sample, red over grey, default otherwise', () => {
    expect(markClass({ red: true, short: false })).toBe('sq-red')
    expect(markClass({ red: true, short: true })).toBe('sq-red')
    expect(markClass({ red: false, short: true })).toBe('sq-text-3')
    expect(markClass({ red: false, short: false })).toBe('')
  })
})

describe('statusText (FINAL §2, §5)', () => {
  const now = local(13, 21, 30)
  it('adds 恢复于 time to 观察中 only while the recovery is inside the 60-minute window', () => {
    const recent = new Date(local(13, 21, 2)).toISOString()
    const old = new Date(local(13, 20, 20)).toISOString()
    expect(statusText({ status: 'observing', stop: null, reset_at: recent }, now, t, te))
      .toBe('admin.schedQuality.state.observing · admin.schedQuality.recover.at:{"time":"21:02"}')
    expect(statusText({ status: 'observing', stop: null, reset_at: old }, now, t, te)).toBe('admin.schedQuality.state.observing')
    expect(statusText({ status: 'observing', stop: null, reset_at: null }, now, t, te)).toBe('admin.schedQuality.state.observing')
  })

  it('writes a stop as 停用·reason·until, the fallback word for an unknown reason, and no time without an end', () => {
    const until = new Date(local(14, 14, 20)).toISOString()
    expect(statusText({ status: 'disabled', stop: { source: 'auto', reason: 'rate_limit', until }, reset_at: null }, now, t, te))
      .toBe('admin.schedQuality.state.disabled·admin.schedQuality.kind.rate_limit·11-14 14:20')
    expect(statusText({ status: 'disabled', stop: { source: 'manual', reason: 'brand_new', until: null }, reset_at: null }, now, t, te))
      .toBe('admin.schedQuality.state.disabled·admin.schedQuality.unknown')
    expect(statusText({ status: 'normal', stop: null, reset_at: null }, now, t, te)).toBe('admin.schedQuality.state.normal')
  })
})

describe('drawer cells', () => {
  const row = (patch: Partial<SchedGroupModelDetail> = {}): SchedGroupModelDetail => ({
    model: 'm', failing: false, dims: [], level: 0, since: null, until: null, other_group_ids: [], exempt: false,
    ttfo: { scope: 'own', window_min: 30, n: 40, bad: 9, lcb: 0.12, verdict: 'fail' },
    error: { scope: 'merged', window_min: 120, n: 80, bad: 2, lcb: 0.004, verdict: 'pass' },
    cache: { scope: 'own', window_min: 60, n: 12, ratio: 0.615, lcb: null, verdict: 'short' },
    ...patch
  })

  it('shows first output as over/n, error as a rate and cache as a ratio, coloured by the verdict', () => {
    expect(dimDetail(row(), 'ttfo')).toEqual({ text: '9/40', n: null, cls: 'sq-red', merged: false })
    expect(dimDetail(row(), 'error')).toEqual({ text: '2.5%', n: 80, cls: '', merged: true })
    expect(dimDetail(row(), 'cache')).toEqual({ text: '62%', n: 12, cls: 'sq-text-3', merged: false })
  })

  it('shows a grey dash without samples', () => {
    expect(dimDetail(row({ error: { scope: 'own', window_min: 60, n: 0, bad: 0, lcb: null, verdict: 'short' } }), 'error'))
      .toEqual({ text: '—', n: null, cls: 'sq-text-3', merged: false })
  })

  it('writes the three lines of a group', () => {
    expect(policyParts({ ttft_seconds: 12.5, max_error_rate: 0.05, min_cache_ratio: 0.875 })).toEqual({ ttfo: '12.5s', error: '5%', cache: '88%' })
  })
})
