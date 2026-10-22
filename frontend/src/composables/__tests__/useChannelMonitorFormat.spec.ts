import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'

describe('useChannelMonitorFormat.statusBadgeClass', () => {
  const { statusBadgeClass } = useChannelMonitorFormat()

  it('draws error and failed in red, like the timeline', () => {
    expect(statusBadgeClass('error')).toContain('bg-red-100')
    expect(statusBadgeClass('failed')).toContain('bg-red-100')
  })

  it('keeps green, yellow and neutral for the other states', () => {
    expect(statusBadgeClass('operational')).toContain('bg-emerald-100')
    expect(statusBadgeClass('degraded')).toContain('bg-amber-100')
    expect(statusBadgeClass('')).toContain('bg-gray-100')
  })
})
