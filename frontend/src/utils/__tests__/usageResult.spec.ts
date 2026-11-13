import { describe, expect, it } from 'vitest'

import { errorClassWord, outcomeWord, resultClass, resultWord } from '../usageResult'

const t = (key: string) => key
const te = (key: string) => key.endsWith('.STALL')

describe('usage results (FINAL §6, contract §0)', () => {
  it('names the four request results, a dash before the upgrade, and colours only a failure red', () => {
    expect([1, 2, 3, 4].map((r) => resultWord(r as 1 | 2 | 3 | 4, t))).toEqual([
      'admin.schedQuality.usage.result.success', 'admin.schedQuality.usage.result.rescued',
      'admin.schedQuality.usage.result.failed', 'admin.schedQuality.usage.result.canceled'
    ])
    expect(resultWord(null, t)).toBe('—')
    expect([1, 2, 3, 4, null].map((r) => resultClass(r as 1 | 2 | 3 | 4 | null))).toEqual(['sq-text', 'sq-text', 'sq-cool', 'sq-text-3', 'sq-text-3'])
  })

  it('names attempt outcomes 1, 2, 3, 5 and falls back for anything else', () => {
    expect([1, 2, 3, 5, 4].map((o) => outcomeWord(o, t))).toEqual([
      'admin.schedQuality.usage.outcome.success', 'admin.schedQuality.usage.outcome.timeout',
      'admin.schedQuality.usage.outcome.failed', 'admin.schedQuality.usage.outcome.canceled', 'admin.schedQuality.unknown'
    ])
  })

  it('maps an error class to its short word, an unknown class to the fallback word, none to a dash', () => {
    expect(errorClassWord('STALL', t, te)).toBe('admin.schedQuality.usage.errorClass.STALL')
    expect(errorClassWord('BRAND_NEW', t, te)).toBe('admin.schedQuality.unknown')
    expect(errorClassWord(null, t, te)).toBe('—')
  })
})
