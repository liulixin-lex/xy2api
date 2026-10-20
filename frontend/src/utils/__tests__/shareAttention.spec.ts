import { describe, expect, it } from 'vitest'
import { namedWarnings, shareAttention, shareFailsOften } from '../shareAttention'
import { shareMember, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

const codes = (items: ReturnType<typeof shareAttention>) => items.map(item => item.code)

describe('shareAttention', () => {
  it('is empty for a balanced, healthy group', () => {
    expect(shareAttention(shareView(5, { scheduler: 'share' }), true)).toEqual([])
  })

  it('flags the main switch only for share groups', () => {
    expect(codes(shareAttention(shareView(5, { scheduler: 'share' }), false))).toEqual(['global_switch_off'])
    expect(shareAttention(shareView(5, { scheduler: 'sub2api' }), false)).toEqual([])
  })

  it('flags a group without a healthy account', () => {
    const view = shareView(5, { active_priority: null, members: [shareMember(1, { healthy: false, expected_share: 0 })] })
    expect(codes(shareAttention(view, true))).toEqual(['no_healthy_account'])
  })

  it('names the top priority and where new sessions go instead', () => {
    const view = shareView(5, {
      active_priority: 2,
      members: [shareMember(1, { priority: 1, healthy: false, expected_share: 0 }), shareMember(2, { priority: 2, expected_share: 1 })]
    })
    expect(shareAttention(view, true)).toEqual([
      expect.objectContaining({ code: 'top_priority_unhealthy', severity: 'warn', priority: 1, value: 2 })
    ])
  })

  it('flags one account taking most new sessions only when it has company', () => {
    const crowded = shareView(5, { members: [shareMember(1, { expected_share: 0.9 }), shareMember(2, { expected_share: 0.1 })] })
    expect(shareAttention(crowded, true)).toEqual([
      expect.objectContaining({ code: 'single_account_dominant', account_id: 1, name: 'acct-1', value: 0.9 })
    ])
    const alone = shareView(5, { members: [shareMember(1, { expected_share: 1 }), shareMember(2, { healthy: false, expected_share: 0 })] })
    expect(shareAttention(alone, true)).toEqual([])
  })

  it('suggests action for accounts that fail often, after enough tries', () => {
    const member = { recent_requests_1h: 80, recent_errors_1h: 20, recent_error_rate_1h: 0.2 }
    expect(shareFailsOften(member)).toBe(true)
    expect(shareFailsOften({ recent_requests_1h: 5, recent_errors_1h: 5, recent_error_rate_1h: 0.5 })).toBe(false)
    expect(shareFailsOften({ ...member, recent_error_rate_1h: 0.05 })).toBe(false)
    expect(shareFailsOften({ ...member, recent_error_rate_1h: null })).toBe(false)
    const view = shareView(5, { members: [shareMember(1, member), shareMember(2)] })
    expect(shareAttention(view, true)).toEqual([expect.objectContaining({ code: 'high_failure', account_id: 1, value: 0.2 })])
  })

  it('lists very high load factors as information after the warnings', () => {
    const view = shareView(5, {
      scheduler: 'share',
      members: [shareMember(1, { effective_load_factor: 999, load_factor: 999, healthy: false, expected_share: 0 }), shareMember(2, { expected_share: 1 })]
    })
    const items = shareAttention(view, false)
    expect(codes(items)).toEqual(['global_switch_off', 'high_load_factor'])
    expect(items[1]).toMatchObject({ severity: 'info', account_id: 1, value: 999 })
  })
})

describe('namedWarnings', () => {
  it('attaches account names and severities to server warnings', () => {
    const items = namedWarnings([
      { code: 'high_load_factor', account_id: 3, priority: null, value: 9999 },
      { code: 'top_priority_unhealthy', account_id: null, priority: 1, value: 2 }
    ], new Map([[3, 'acct-3']]))
    expect(items).toEqual([
      expect.objectContaining({ code: 'high_load_factor', severity: 'info', name: 'acct-3', value: 9999 }),
      expect.objectContaining({ code: 'top_priority_unhealthy', severity: 'warn', priority: 1, value: 2 })
    ])
  })
})
