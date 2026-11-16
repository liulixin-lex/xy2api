import { describe, expect, it } from 'vitest'

import type { Account } from '@/types'
import { accountMatchesFilters, qualityMatches } from '../accountFilters'
import type { AccountQualityView } from '../accountGroups'

const quality = (patch: Partial<AccountQualityView> = {}): AccountQualityView => ({
  status: 'normal',
  stop: null,
  rear: [],
  exemptions: [],
  ...patch
})
const substandard = (effective = true) => ({ group_id: 1, model: 'gpt-6', lines: ['error' as const], until: '', exempt: !effective, effective })
const stop = { source: 'auto', reason: 'error', until: null }

// The four options as the backend filters them (sched_admin_summary.go, attention), spec 7e and 9.5–9.6.
describe('quality filter', () => {
  it('matches each of normal, back of queue, exempt and observing the way the backend filters', () => {
    expect(qualityMatches('openai', quality(), 'normal')).toBe(true)
    expect(qualityMatches('openai', quality({ rear: [substandard()] }), 'substandard')).toBe(true)
    expect(qualityMatches('openai', quality({ rear: [substandard()] }), 'normal')).toBe(false)
    expect(qualityMatches('openai', quality({ exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }), 'exempt')).toBe(true)
    expect(qualityMatches('openai', quality({ status: 'observing' }), 'observing')).toBe(true)
    expect(qualityMatches('openai', quality({ status: 'observing' }), 'normal')).toBe(false)
  })

  it('takes only records in effect for the back of queue, and none while the account is stopped', () => {
    expect(qualityMatches('openai', quality({ rear: [substandard(false)] }), 'substandard')).toBe(false)
    expect(qualityMatches('openai', quality({ rear: [substandard(false)] }), 'normal')).toBe(true)
    expect(qualityMatches('openai', quality({ status: 'disabled', stop, substandard: [substandard()] }), 'substandard')).toBe(false)
  })

  it('calls normal only an OpenAI account with status normal, no exemption and no record in effect', () => {
    expect(qualityMatches('openai', quality({ exemptions: [{ group_id: 1, expires_at: null, reason: '' }] }), 'normal')).toBe(false)
    expect(qualityMatches('openai', quality({ status: 'disabled', stop }), 'normal')).toBe(false)
    expect(qualityMatches('gemini', quality(), 'normal')).toBe(false)
    expect(qualityMatches('openai', null, 'normal')).toBe(false)
  })

  it('matches none of the four for an account outside OpenAI or without a summary', () => {
    for (const filter of ['normal', 'substandard', 'exempt', 'observing']) {
      expect(qualityMatches('anthropic', quality({ exemptions: [{ group_id: 0, expires_at: null, reason: '' }], status: 'observing' }), filter)).toBe(false)
      expect(qualityMatches('openai', null, filter)).toBe(false)
    }
  })

  it('judges nothing before the row\'s first summary is in', () => {
    expect(qualityMatches('openai', undefined, 'substandard')).toBe(true)
    expect(qualityMatches('openai', undefined, 'normal')).toBe(true)
  })

  it('takes part in the row match next to the other filters', () => {
    const account = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [1] } as unknown as Account
    const context = { now: () => Date.now(), currentTime: () => Date.now(), matchesCategory: () => true }
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality({ rear: [substandard()] }) })).toBe(true)
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality() })).toBe(false)
    expect(accountMatchesFilters(account, { platform: 'gemini' }, { ...context, quality: quality() })).toBe(false)
  })
})

describe('row match', () => {
  const base = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true } as unknown as Account

  // 7d-1: the row's group_ids is its only group list; the full structure's groups are no fallback.
  it('reads the groups from group_ids only: a row without them is ungrouped even with stale group objects', () => {
    const context = { now: () => 0, currentTime: () => 0, matchesCategory: () => true }
    const stale = { ...base, groups: [{ id: 3, name: 'g3' }] } as unknown as Account
    expect(accountMatchesFilters(stale, { group: 'ungrouped' }, context)).toBe(true)
    expect(accountMatchesFilters(stale, { group: '3' }, context)).toBe(false)
    expect(accountMatchesFilters({ ...base, group_ids: [3] } as Account, { group: '3' }, context)).toBe(true)
  })

  // 7d-10: a rate limit that ends turns the "rate limited" filter's row into a mismatch at that second. 7g-1: only a
  // time still ahead reads the ticking clock; one already past (the backend keeps it) does not.
  it('judges the time filters on the clock it is given, and reads the ticking clock only for a time still ahead', () => {
    let now = 1_000
    const reads = { n: 0 }
    const context = { now: () => { reads.n++; return now }, currentTime: () => now, matchesCategory: () => true }
    const limited = { ...base, rate_limit_reset_at: new Date(2_000).toISOString() } as Account
    expect(accountMatchesFilters(limited, { status: 'rate_limited' }, context)).toBe(true)
    expect(reads.n).toBe(1)
    now = 3_000
    reads.n = 0
    expect(accountMatchesFilters(limited, { status: 'rate_limited' }, context)).toBe(false)
    accountMatchesFilters(base, { status: 'rate_limited' }, context)
    expect(reads.n).toBe(0)
  })
})
