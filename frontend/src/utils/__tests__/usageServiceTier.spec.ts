import { describe, expect, it } from 'vitest'

import { formatUsageServiceTier, getUsageServiceTierLabel, normalizeUsageServiceTier } from '@/utils/usageServiceTier'

describe('usageServiceTier utils', () => {
  it('normalizes fast/default aliases', () => {
    expect(normalizeUsageServiceTier('fast')).toBe('priority')
    expect(normalizeUsageServiceTier(' default ')).toBe('standard')
    expect(normalizeUsageServiceTier('STANDARD')).toBe('standard')
  })

  it('preserves supported tiers', () => {
    expect(normalizeUsageServiceTier('priority')).toBe('priority')
    expect(normalizeUsageServiceTier('flex')).toBe('flex')
    expect(normalizeUsageServiceTier('ultrafast')).toBe('ultrafast')
  })

  it('preserves missing historical tiers', () => {
    expect(formatUsageServiceTier()).toBe('')
    expect(formatUsageServiceTier('')).toBe('')
  })

  it('passes through unknown non-empty tiers for display fallback', () => {
    expect(normalizeUsageServiceTier('custom-tier')).toBe('custom-tier')
    expect(formatUsageServiceTier('custom-tier')).toBe('custom-tier')
  })

  it('maps tiers to translated labels', () => {
    const translate = (key: string) => ({
      'usage.serviceTierPriority': 'Fast',
      'usage.serviceTierUltrafast': 'Ultrafast',
      'usage.serviceTierFlex': 'Flex',
      'usage.serviceTierStandard': 'Standard',
      'usage.tierDetails.notRecorded': 'Not recorded',
    })[key] ?? key

    expect(getUsageServiceTierLabel('fast', translate)).toBe('Fast')
    expect(getUsageServiceTierLabel('ultrafast', translate)).toBe('Ultrafast')
    expect(getUsageServiceTierLabel('flex', translate)).toBe('Flex')
    expect(getUsageServiceTierLabel(undefined, translate)).toBe('Not recorded')
    expect(getUsageServiceTierLabel('custom-tier', translate)).toBe('custom-tier')
  })
})
