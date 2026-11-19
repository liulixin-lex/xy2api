import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import UsageStatsCards from '../UsageStatsCards.vue'

// Browser check #4 with the real messages: the user-wait card's request count picks the English plural form, so the
// call must pass the count (the copy stubs elsewhere drop it). Chinese has one form.
const stats = {
  total_requests: 1, total_input_tokens: 0, total_output_tokens: 0, total_cache_tokens: 0, total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, total_account_cost: 0, average_duration_ms: 0
}
function note(locale: 'en' | 'zh', value: Record<string, unknown>) {
  const i18n = createI18n({ legacy: false, locale, messages: { en, zh } as never })
  const wrapper = mount(UsageStatsCards, {
    props: { stats: { ...stats, ...value } as never, e2eState: 'loaded' },
    global: { plugins: [i18n], stubs: { Icon: true } }
  })
  return wrapper.get('[data-testid="usage-e2e-samples"]').text()
}

describe('UsageStatsCards request count plural (real messages)', () => {
  it('says "Based on 1 request" for one and "requests" for more, with and without a median', () => {
    expect(note('en', { e2e_first_output_p50_ms: 1850, e2e_first_output_samples: 1 })).toBe('Based on 1 request')
    expect(note('en', { e2e_first_output_p50_ms: 1850, e2e_first_output_samples: 2 })).toBe('Based on 2 requests')
    expect(note('en', { e2e_first_output_samples: 1 })).toBe('Based on 1 request, but the answer has no median')
    expect(note('en', { e2e_first_output_samples: 7 })).toBe('Based on 7 requests, but the answer has no median')
    expect(note('zh', { e2e_first_output_p50_ms: 1850, e2e_first_output_samples: 1 })).toBe('基于 1 个请求')
  })
})
