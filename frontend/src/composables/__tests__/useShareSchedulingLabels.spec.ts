import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent } from 'vue'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import { useShareSchedulingLabels } from '../useShareSchedulingLabels'
import ShareReportDetails from '@/components/admin/shareScheduling/ShareReportDetails.vue'
import { shareReport } from '@/stores/__tests__/shareSchedulingFixtures'

function withI18n<T>(use: () => T): T {
  let result: T | undefined
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh } })
  mount(defineComponent({ setup() { result = use(); return () => null } }), { global: { plugins: [i18n] } })
  return result as T
}

// The test build of vue-i18n is runtime-only: t() returns the key, while te()
// still checks the real locale messages, which is what the fallback relies on.
describe('useShareSchedulingLabels', () => {
  it('translates known codes and shows unknown or retired ones as they are', () => {
    const labels = withI18n(() => useShareSchedulingLabels())
    expect(labels.reason('affinity_hit')).toBe('admin.shareScheduling.reasons.affinity_hit')
    // Counters written before the upgrade can still carry retired codes.
    expect(labels.reason('original')).toBe('original')
    expect(labels.reason('drain_cold')).toBe('drain_cold')
    expect(labels.keySource('key_prefix')).toBe('key_prefix')
    expect(labels.keySource('key_prompt_cache_key')).toBe('key_prompt_cache_key')
    expect(labels.cacheClass('miss_something_new')).toBe('miss_something_new')
    expect(labels.reason(null)).toBe('-')
    expect(labels.risk({ code: 'brand_new_warning' })).toBe('brand_new_warning')
  })

  it('keeps unknown codes in the detailed report instead of dropping them', () => {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh } })
    const report = shareReport(6, {
      details: {
        cache_classes: { cache_hit: { requests: 3, tokens: 300 }, legacy_class: { requests: 1, tokens: 100 } },
        reasons: { affinity_hit: 5, original: 2 },
        waits: null,
        attempts: null,
        share_requests: null
      },
      accounts: [{ account_id: 1, requests: 5, request_share: 1, tokens: 0, token_share: null, standard_cost: 0, cost_share: null, skipped: { old_skip_reason: 4 } }]
    })
    const w = mount(ShareReportDetails, { props: { report }, global: { plugins: [i18n] } })
    const text = w.text()
    expect(text).toContain('legacy_class')
    expect(text).toContain('original')
    expect(text).toContain('old_skip_reason')
    expect(text).toContain('admin.shareScheduling.reasons.affinity_hit')
  })
})
