import { describe, expect, it } from 'vitest'

import type { Account } from '@/types'
import { accountMatchesFilters, qualityMatches } from '../accountFilters'
import type { AccountQualityView } from '../accountGroups'

const quality = (patch: Partial<AccountQualityView> = {}): AccountQualityView => ({
  group_status: { 1: 'pass' },
  stop: null,
  rear: [],
  trial: [],
  exemptions: [],
  ...patch
})
const stop = { source: 'auto', reason: 'error', until: null }
const exempt = [{ group_id: 0, expires_at: null, reason: '' }]

// The five options as the backend filters them (H.11), from the groups' identities.
describe('quality filter', () => {
  it('matches rear, trial and unknown by any group with that identity, exempt by any exemption', () => {
    const mixed = quality({ group_status: { 1: 'pass', 2: 'rear', 3: 'trial' } })
    expect(['normal', 'rear', 'trial', 'unknown', 'exempt'].map((filter) => qualityMatches('openai', mixed, filter))).toEqual([false, true, true, false, false])
    const unknown = quality({ group_status: { 1: 'unknown' }, exemptions: exempt })
    expect(['normal', 'rear', 'trial', 'unknown', 'exempt'].map((filter) => qualityMatches('openai', unknown, filter))).toEqual([false, false, false, true, true])
  })

  it('calls normal only an account not stopped with every group passing, exemptions aside, and at least one group', () => {
    expect(qualityMatches('openai', quality({ group_status: { 1: 'pass', 2: 'pass' } }), 'normal')).toBe(true)
    expect(qualityMatches('openai', quality({ group_status: { 1: 'pass' }, exemptions: exempt }), 'normal')).toBe(true)
    expect(qualityMatches('openai', quality({ group_status: { 1: 'pass', 2: 'unknown' } }), 'normal')).toBe(false)
    expect(qualityMatches('openai', quality({ group_status: {} }), 'normal')).toBe(false)
    expect(qualityMatches('openai', quality({ stop }), 'normal')).toBe(false)
  })

  it('takes no stopped account for the rear, while trial and unknown still read the identity', () => {
    const stopped = quality({ stop, group_status: { 1: 'rear', 2: 'trial' } })
    expect(qualityMatches('openai', stopped, 'rear')).toBe(false)
    expect(qualityMatches('openai', stopped, 'trial')).toBe(true)
  })

  it('matches none of the five for an account outside OpenAI or without a summary', () => {
    for (const filter of ['normal', 'rear', 'trial', 'unknown', 'exempt']) {
      expect(qualityMatches('anthropic', quality({ exemptions: exempt, group_status: { 1: 'rear', 2: 'trial', 3: 'unknown' } }), filter)).toBe(false)
      expect(qualityMatches('openai', null, filter)).toBe(false)
    }
  })

  it('judges nothing before the row\'s first summary is in', () => {
    expect(qualityMatches('openai', undefined, 'rear')).toBe(true)
    expect(qualityMatches('openai', undefined, 'normal')).toBe(true)
  })

  it('takes part in the row match next to the other filters', () => {
    const account = { id: 1, name: 'a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [1] } as unknown as Account
    const context = { now: () => Date.now(), currentTime: () => Date.now(), matchesCategory: () => true }
    expect(accountMatchesFilters(account, { sched_quality: 'rear' }, { ...context, quality: quality({ group_status: { 1: 'rear' } }) })).toBe(true)
    expect(accountMatchesFilters(account, { sched_quality: 'rear' }, { ...context, quality: quality() })).toBe(false)
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
