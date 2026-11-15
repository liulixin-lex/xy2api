import { describe, expect, it } from 'vitest'

import type { Account } from '@/types'
import { accountRowPatch } from '../accountRow'

// A full account written into the row (7d-1): what the backend omits when empty is written as empty.
describe('accountRowPatch', () => {
  const full = (patch: Record<string, unknown>) => ({ id: 7, name: 'acc-7', platform: 'openai', status: 'active', ...patch }) as unknown as Account

  it('writes group_ids as [] when the answer leaves them out, and keeps the full structure out of the row', () => {
    const patch = accountRowPatch(full({ groups: [{ id: 3, name: 'Gamma' }], account_groups: [{ group_id: 3 }] })) as Record<string, unknown>
    expect(patch.group_ids).toEqual([])
    expect('groups' in patch).toBe(false)
    expect('account_groups' in patch).toBe(false)
  })

  it('writes every other omitted list field as empty, and keeps the values the answer has', () => {
    const patch = accountRowPatch(full({ group_ids: [1, 2], max_sessions: 4 })) as Record<string, unknown>
    expect(patch.group_ids).toEqual([1, 2])
    expect(patch.max_sessions).toBe(4)
    expect(patch.name).toBe('acc-7')
    // Present as keys, so a merge clears what the row showed before.
    expect('window_cost_limit' in patch && patch.window_cost_limit === undefined).toBe(true)
    expect('proxy' in patch && patch.proxy === undefined).toBe(true)
  })
})
