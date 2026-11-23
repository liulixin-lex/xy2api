import { describe, expect, it } from 'vitest'
import { unbilledWord } from '../usageRow'
import type { AdminUsageLog } from '@/types'

const t = (key: string) => key
const row = (result: number | null) => ({ result } as unknown as AdminUsageLog)

describe('unbilledWord', () => {
  it('names every result a request without a bill can have', () => {
    expect(unbilledWord(row(3), t)).toBe('admin.usage.resultFailed')
    expect(unbilledWord(row(4), t)).toBe('admin.usage.resultCanceled')
    expect(unbilledWord(row(5), t)).toBe('admin.usage.resultLocalEnded')
    expect(unbilledWord(row(6), t)).toBe('admin.usage.resultLocalUndeliverable')
  })

  it('shows no word for billed results or a missing result', () => {
    expect(unbilledWord(row(1), t)).toBe('')
    expect(unbilledWord(row(2), t)).toBe('')
    expect(unbilledWord(row(null), t)).toBe('')
  })
})
