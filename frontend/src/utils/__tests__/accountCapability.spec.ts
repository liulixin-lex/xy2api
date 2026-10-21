import { describe, expect, it } from 'vitest'
import { activeStreamOnly, type AccountCapabilityLearned } from '../accountCapability'

const NOW = Date.parse('2026-10-21T03:00:00Z')
const live = { protocol: 'responses', mode: 'stream_only', expires_at: '2026-10-21T05:00:00Z' }

describe('activeStreamOnly', () => {
  // A cleared state is written as JSON null (A-P2-7): null or missing means no record.
  it('treats null or missing as no record', () => {
    expect(activeStreamOnly(null, NOW)).toEqual([])
    expect(activeStreamOnly(undefined, NOW)).toEqual([])
    const holes = [null, live, { protocol: 'chat', mode: 'stream_only' }, { protocol: 'chat', mode: 'stream_only', expires_at: null }] as unknown as AccountCapabilityLearned[]
    expect(activeStreamOnly(holes, NOW)).toEqual([live])
  })

  it('keeps unexpired stream-only entries, latest expiry first', () => {
    const soon = { protocol: 'chat', mode: 'stream_only', expires_at: '2026-10-21T03:30:00Z' }
    const gone = { protocol: 'chat', mode: 'stream_only', expires_at: '2026-10-21T02:59:59Z' }
    const other = { protocol: 'chat', mode: 'manual', expires_at: '2026-10-21T09:00:00Z' }
    expect(activeStreamOnly([soon, gone, other, live], NOW)).toEqual([live, soon])
  })
})
