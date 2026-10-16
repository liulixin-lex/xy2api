import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ResilienceExplainResult from '../ResilienceExplainResult.vue'
import type { ResilienceExplanation } from '@/api/admin/resilience'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => !key.endsWith('.mystery')
  })
}))
vi.mock('@/utils/format', () => ({ formatDateTime: (d: string) => `at ${d}` }))

const base: ResilienceExplanation = {
  group_id: 10,
  enabled: true,
  healthy_count: 0,
  accounts: [
    { account_id: 4, name: 'dry', platform: 'openai', priority: 1, tier: 'hard', reason: 'balance_exhausted', balance_state: 'EXHAUSTED' },
    { account_id: 1, name: 'busy', platform: 'openai', priority: 2, tier: 'soft', reason: 'mystery', recover_at: '2026-10-15T08:00:00Z', session_failures: 2, avoided: true }
  ],
  last_resort: { account_id: 1, recover_at: '2026-10-15T08:00:00Z' },
  fallback_group: { group_id: 20, name: 'backup', usable: true, fallback_healthy: true, would_move: true },
  session: { failures: 2, avoided: [1] }
}

const text = (w: ReturnType<typeof mount>, id: string) => w.get(`[data-testid="resilience-explain-${id}"]`).text()

describe('resilience explain result', () => {
  it('shows tiers, reasons, the session part, last resort and the fallback move', () => {
    const w = mount(ResilienceExplainResult, { props: { explanation: base } })
    const rows = w.get('[data-testid="resilience-explain-accounts"]').findAll('tbody tr')
    expect(rows[0].text()).toContain('dry #4')
    expect(rows[0].text()).toContain('admin.resilience.explain.tiers.hard')
    expect(rows[0].text()).toContain('admin.resilience.explain.reasons.balance_exhausted')
    expect(rows[0].text()).toContain('admin.resilience.panel.states.EXHAUSTED')
    expect(rows[1].text()).toContain('mystery')
    expect(rows[1].text()).toContain('at 2026-10-15T08:00:00Z')
    expect(rows[1].text()).toContain('admin.resilience.explain.yes')
    expect(text(w, 'healthy')).toBe('0')
    expect(text(w, 'lastResort')).toContain('busy #1')
    expect(text(w, 'fallback')).toBe('admin.resilience.explain.fallbackMove:{"group":"backup #20"}')
    expect(text(w, 'sessionFailures')).toBe('2')
    expect(text(w, 'sessionAvoided')).toBe('busy #1')
  })

  it('describes fallback groups that stay and hides the session part without a session', () => {
    const stay = { ...base, healthy_count: 1, last_resort: undefined, session: undefined }
    const cases: Array<[ResilienceExplanation['fallback_group'], string]> = [
      [undefined, 'fallbackNone'],
      [{ group_id: 20, name: 'backup', usable: false, fallback_healthy: true, would_move: false }, 'fallbackUnusable'],
      [{ group_id: 20, name: 'backup', usable: true, fallback_healthy: false, would_move: false }, 'fallbackUnhealthy'],
      [{ group_id: 20, name: 'backup', usable: true, fallback_healthy: true, would_move: false }, 'fallbackStay']
    ]
    for (const [fallback, key] of cases) {
      const w = mount(ResilienceExplainResult, { props: { explanation: { ...stay, fallback_group: fallback } } })
      expect(text(w, 'fallback')).toContain(`admin.resilience.explain.${key}`)
      expect(text(w, 'lastResort')).toBe('admin.resilience.explain.none')
      expect(w.find('[data-testid="resilience-explain-sessionFailures"]').exists()).toBe(false)
    }
  })

  it('names a model restriction above the accounts', () => {
    const plain = mount(ResilienceExplainResult, { props: { explanation: base } })
    expect(plain.find('[data-testid="resilience-explain-restriction"]').exists()).toBe(false)
    const w = mount(ResilienceExplainResult, { props: { explanation: { ...base, model_restriction: 'channel_pricing' } } })
    expect(text(w, 'restriction')).toBe('admin.resilience.explain.restriction.channel_pricing')
  })
})
