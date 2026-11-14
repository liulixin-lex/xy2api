import { describe, expect, it } from 'vitest'

import type { Account } from '@/types'
import { accountMatchesFilters, qualityMatches } from '../accountFilters'
import type { AccountQualityView } from '../accountGroups'

const quality = (patch: Partial<AccountQualityView> = {}): AccountQualityView => ({
  status: 'normal',
  stop: null,
  substandard: [],
  exemptions: [],
  ...patch
})
const substandard = (exempt = false) => ({ group_id: 1, model: 'gpt-6', dims: ['error' as const], until: '', other_group: false, exempt })

describe('quality filter', () => {
  it('matches each of normal, substandard, exempt and observing the way the backend filters', () => {
    expect(qualityMatches(quality(), 'normal')).toBe(true)
    expect(qualityMatches(quality({ substandard: [substandard()] }), 'substandard')).toBe(true)
    expect(qualityMatches(quality({ substandard: [substandard()] }), 'normal')).toBe(false)
    expect(qualityMatches(quality({ exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }), 'exempt')).toBe(true)
    expect(qualityMatches(quality({ status: 'observing' }), 'observing')).toBe(true)
    expect(qualityMatches(quality({ status: 'observing' }), 'normal')).toBe(false)
  })

  it('does not count an exempt or stopped account\'s substandard models as substandard', () => {
    expect(qualityMatches(quality({ substandard: [substandard(true)] }), 'substandard')).toBe(false)
    const stopped = quality({ stop: { source: 'auto', reason: 'error', until: null }, substandard: [substandard()] })
    expect(qualityMatches(stopped, 'substandard')).toBe(false)
    expect(qualityMatches(stopped, 'normal')).toBe(true)
  })

  it('matches while the summary has not arrived', () => {
    expect(qualityMatches(undefined, 'substandard')).toBe(true)
  })

  it('takes part in the row match next to the other filters', () => {
    const account = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [1] } as unknown as Account
    const context = { now: () => Date.now(), matchesCategory: () => true }
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality({ substandard: [substandard()] }) })).toBe(true)
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality() })).toBe(false)
    expect(accountMatchesFilters(account, { platform: 'gemini' }, { ...context, quality: quality() })).toBe(false)
  })

  // 7d-2: with quality scheduling off or its view stale the backend puts nobody at the back.
  it('does not take a substandard model for the back while quality scheduling is off or stale: "normal" keeps the row', () => {
    const listed = quality({ substandard: [substandard()] })
    expect(qualityMatches(listed, 'normal', false)).toBe(true)
    expect(qualityMatches(listed, 'substandard', false)).toBe(false)
    const account = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [1] } as unknown as Account
    expect(accountMatchesFilters(account, { sched_quality: 'normal' }, { now: () => 0, matchesCategory: () => true, quality: listed, qualityActive: false })).toBe(true)
  })
})

describe('row match', () => {
  const base = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true } as unknown as Account

  // 7d-1: the row's group_ids is its only group list; the full structure's groups are no fallback.
  it('reads the groups from group_ids only: a row without them is ungrouped even with stale group objects', () => {
    const context = { now: () => 0, matchesCategory: () => true }
    const stale = { ...base, groups: [{ id: 3, name: 'g3' }] } as unknown as Account
    expect(accountMatchesFilters(stale, { group: 'ungrouped' }, context)).toBe(true)
    expect(accountMatchesFilters(stale, { group: '3' }, context)).toBe(false)
    expect(accountMatchesFilters({ ...base, group_ids: [3] } as Account, { group: '3' }, context)).toBe(true)
  })

  // 7d-10: a rate limit that ends turns the "rate limited" filter's row into a mismatch at that second.
  it('judges the time filters on the clock it is given, and reads it only for a row with such a time', () => {
    let now = 1_000
    const reads = { n: 0 }
    const clock = () => { reads.n++; return now }
    const limited = { ...base, rate_limit_reset_at: new Date(2_000).toISOString() } as Account
    expect(accountMatchesFilters(limited, { status: 'rate_limited' }, { now: clock, matchesCategory: () => true })).toBe(true)
    now = 3_000
    expect(accountMatchesFilters(limited, { status: 'rate_limited' }, { now: clock, matchesCategory: () => true })).toBe(false)
    reads.n = 0
    accountMatchesFilters(base, { status: 'rate_limited' }, { now: clock, matchesCategory: () => true })
    expect(reads.n).toBe(0)
  })
})
