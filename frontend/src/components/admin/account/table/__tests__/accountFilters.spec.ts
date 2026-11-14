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
    const context = { now: Date.now(), matchesCategory: () => true }
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality({ substandard: [substandard()] }) })).toBe(true)
    expect(accountMatchesFilters(account, { sched_quality: 'substandard' }, { ...context, quality: quality() })).toBe(false)
    expect(accountMatchesFilters(account, { platform: 'gemini' }, { ...context, quality: quality() })).toBe(false)
  })
})
