import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ShareSchedulingView from '../ShareSchedulingView.vue'
import { shareGroup, shareMember, shareMetrics, sharePreview, shareReport, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  listGroups: vi.fn(),
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  preview: vi.fn(),
  explain: vi.fn(),
  report: vi.fn()
}))
const accounts = vi.hoisted(() => ({ update: vi.fn() }))
const router = vi.hoisted(() => ({ replace: vi.fn(), query: {} as Record<string, string> }))

vi.mock('@/api/admin/shareScheduling', () => ({ default: api }))
vi.mock('@/api/admin/accounts', async importOriginal => ({ ...(await importOriginal<typeof import('@/api/admin/accounts')>()), update: accounts.update }))
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params && Object.keys(params).length ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true
  })
}))
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/admin/share-scheduling', query: router.query }),
  useRouter: () => ({ replace: router.replace }),
  RouterLink: { props: ['to'], template: '<a :href="String(to)"><slot /></a>' }
}))

const groups = [
  shareGroup(5, { name: 'Claude', configured: true, version: 1, member_count: 3, cache_hit_ratio_1h: 0.8 }),
  shareGroup(6, { name: 'Codex', scheduler: 'share', configured: true, version: 3, effective: true }),
  shareGroup(7, { name: 'Images' })
]

function shareGroupView(id = 6) {
  return shareView(id, {
    group_name: 'Codex',
    scheduler: 'share',
    effective: true,
    version: 3,
    members: [
      shareMember(1, { name: 'alpha', expected_share: 0.75, effective_load_factor: 30, load_factor: 30, other_group_ids: [5, 7] }),
      shareMember(2, { name: 'beta', expected_share: 0.25, recent_requests_1h: 60, recent_errors_1h: 40, recent_error_rate_1h: 0.4 }),
      shareMember(3, { name: 'gamma', priority: 2, expected_share: 0, share_if_healthy: 0 }),
      shareMember(4, { name: 'delta', healthy: false, reason: 'rate_limited', recover_at: '2026-10-20T10:00:00Z', expected_share: 0, share_if_healthy: 0.2 })
    ]
  })
}

function mountView() {
  return mount(ShareSchedulingView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-dialog><h2>{{ title }}</h2><slot /><slot name="footer" /></div>' },
        Select: { props: ['modelValue', 'options'], template: '<select />' },
        ResilienceExplainResult: true
      }
    }
  })
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  router.query = {}
  api.getSettings.mockResolvedValue({ enabled: true })
  api.updateSettings.mockImplementation(async (s: { enabled: boolean }) => s)
  api.listGroups.mockResolvedValue(groups.map(g => ({ ...g })))
  api.getPolicy.mockImplementation(async (id: number) => (id === 6 ? shareGroupView() : shareView(id, { group_name: id === 5 ? 'Claude' : 'Images' })))
  api.report.mockImplementation(async (id: number) => shareReport(id, { scheduler: id === 6 ? 'share' : 'sub2api', baseline: id === 6 ? shareMetrics() : null }))
  api.preview.mockResolvedValue(sharePreview({ warnings: [{ code: 'high_load_factor', account_id: 1, priority: null, value: 999 }] }))
  api.updatePolicy.mockImplementation(async (id: number, update: { scheduler: 'sub2api' | 'share'; session_keep: boolean; expected_version: number }) =>
    shareView(id, { scheduler: update.scheduler, session_keep: update.session_keep, version: update.expected_version + 1, effective: update.scheduler === 'share' }))
})

describe('share scheduling page', () => {
  it('opens the first group running share scheduling and splits the list', async () => {
    const w = mountView()
    await flushPromises()
    expect(api.getPolicy).toHaveBeenCalledWith(6)
    expect(w.get('[data-testid="share-group-title"]').text()).toBe('Codex')
    expect(w.get('[data-testid="share-group-section-active"]').text()).toContain('Codex')
    expect(w.get('[data-testid="share-group-section-idle"]').text()).toContain('Claude')
    expect(w.get('[data-testid="share-group-section-idle"]').text()).toContain('Images')
  })

  it('opens the group named in the link', async () => {
    router.query = { group: '5' }
    const w = mountView()
    await flushPromises()
    expect(api.getPolicy).toHaveBeenCalledWith(5)
    expect(w.get('[data-testid="share-group-title"]').text()).toBe('Claude')
  })

  it('switches a group to share after confirming the expected shares', async () => {
    router.query = { group: '5' }
    const w = mountView()
    await flushPromises()
    expect(w.get('[data-testid="share-health-sub2api"]').exists()).toBe(true)
    await w.get('[data-testid="share-switch"]').trigger('click')
    await flushPromises()
    expect(api.preview).toHaveBeenCalledWith(5, { scheduler: 'share', session_keep: true, expected_version: 1 })
    const dialog = w.get('[data-testid="share-change"]')
    expect(dialog.text()).toContain('acct-1')
    expect(dialog.text()).toContain('75.0%')
    expect(dialog.text()).toContain('admin.shareScheduling.change.others:{"count":1}')
    expect(w.get('[data-testid="share-change-risk-high_load_factor"]').text()).toContain('"value":"999"')
    expect(api.updatePolicy).not.toHaveBeenCalled()

    await w.get('[data-testid="share-change-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updatePolicy).toHaveBeenCalledWith(5, { scheduler: 'share', session_keep: true, expected_version: 1 })
    expect(w.find('[data-testid="share-change"]').exists()).toBe(false)
    expect(w.get('[data-testid="share-mode-current"]').text()).toBe('admin.shareScheduling.schedulers.share')
  })

  it('goes back to the original scheduler in one confirmation', async () => {
    const w = mountView()
    await flushPromises()
    await w.get('[data-testid="share-rollback"]').trigger('click')
    await flushPromises()
    expect(api.preview).not.toHaveBeenCalled()
    expect(w.get('[data-testid="share-change"]').text()).toContain('admin.shareScheduling.change.sub2apiBody')
    await w.get('[data-testid="share-change-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updatePolicy).toHaveBeenCalledWith(6, { scheduler: 'sub2api', session_keep: true, expected_version: 3 })
  })

  it('asks to reload after another administrator changed the group', async () => {
    const w = mountView()
    await flushPromises()
    api.updatePolicy.mockRejectedValueOnce({ status: 409, reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })
    await w.get('[data-testid="share-session-keep"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="share-change"]').text()).toContain('admin.shareScheduling.change.keepOffBody')
    await w.get('[data-testid="share-change-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updatePolicy).toHaveBeenCalledWith(6, { scheduler: 'share', session_keep: false, expected_version: 3 })
    expect(w.get('[data-testid="share-conflict"]').exists()).toBe(true)
    api.getPolicy.mockClear()
    await w.get('[data-testid="share-conflict-reload"]').trigger('click')
    await flushPromises()
    expect(api.getPolicy).toHaveBeenCalledWith(6)
    expect(w.find('[data-testid="share-conflict"]').exists()).toBe(false)
  })

  it('judges health against the baseline and lists what needs attention', async () => {
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="share-health-level-ok"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-health-metric-cacheHit"]').text()).toContain('80.0%')
    expect(w.get('[data-testid="share-health-metric-cacheHit"]').text()).toContain('admin.shareScheduling.health.referenceOnly')
    const attention = w.get('[data-testid="share-attention"]').text()
    expect(attention).toContain('admin.shareScheduling.risks.high_failure')
    expect(attention).toContain('"name":"beta"')
  })

  it('withholds a verdict while the switch is too recent and says how the switch time was found', async () => {
    api.report.mockImplementation(async (id: number) => shareReport(id, {
      current_from: '2026-10-20T08:40:00Z', current_to: '2026-10-20T09:00:00Z', switched_at: '2026-10-20T08:40:00Z', anchor_source: 'policy_updated_at'
    }))
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="share-health-level-insufficient"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-health-short"]').text()).toContain('"minutes":20')
    expect(w.find('[data-testid="share-health-anchor-estimated"]').exists()).toBe(true)
  })

  it('judges a shortened window once it is long enough and notes how short it is', async () => {
    api.report.mockImplementation(async (id: number) => shareReport(id, {
      current_from: '2026-10-20T08:15:00Z', current_to: '2026-10-20T09:00:00Z', switched_at: '2026-10-20T08:15:00Z'
    }))
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="share-health-level-ok"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-health-truncated"]').text()).toContain('"minutes":45')
    expect(w.find('[data-testid="share-health-anchor-estimated"]').exists()).toBe(false)
  })

  it('shows the traffic split by priority with load factors and other groups', async () => {
    const w = mountView()
    await flushPromises()
    expect(w.get('[data-testid="share-priority-1"]').text()).toContain('admin.shareScheduling.traffic.active')
    expect(w.get('[data-testid="share-priority-2"]').text()).toContain('admin.shareScheduling.traffic.standby')
    expect(w.get('[data-testid="share-member-1"]').text()).toContain('75.0%')
    expect(w.get('[data-testid="share-load-factor-1"]').text()).toContain('"count":2')
    expect(w.get('[data-testid="share-member-4"]').text()).toContain('admin.shareScheduling.reasons.rate_limited')
    expect(w.get('[data-testid="share-member-4"]').text()).toContain('admin.shareScheduling.traffic.ifHealthy')
    expect(w.get('[data-testid="share-member-2"]').text()).toContain('40.0%')
    // Short headers; the hover hint says the window and where the failure rate comes from.
    expect(w.get('[data-testid="share-traffic-actual-header"]').attributes('title')).toBe('admin.shareScheduling.traffic.recentHint')
    expect(w.get('[data-testid="share-traffic-failure-header"]').attributes('title')).toBe('admin.shareScheduling.traffic.recentHint')
  })

  it('explains an unset load factor on hover and in the empty editor', async () => {
    const w = mountView()
    await flushPromises()
    const hint = 'admin.shareScheduling.loadFactor.fromConcurrency:{"value":10}'
    expect(w.get('[data-testid="share-load-factor-value-2"]').attributes('title')).toBe(hint)
    await w.get('[data-testid="share-load-factor-value-2"]').trigger('click')
    const input = w.get('[data-testid="share-load-factor-input-2"]')
    expect((input.element as HTMLInputElement).value).toBe('')
    expect(input.attributes('placeholder')).toBe(hint)
    // A set load factor keeps the plain edit label.
    expect(w.get('[data-testid="share-load-factor-value-1"]').attributes('title')).toContain('admin.shareScheduling.loadFactor.edit')
  })

  it('edits a load factor through the account API and reloads the group', async () => {
    const w = mountView()
    await flushPromises()
    accounts.update.mockResolvedValue({})
    await w.get('[data-testid="share-load-factor-value-1"]').trigger('click')
    expect(w.get('[data-testid="share-load-factor-affects-1"]').text()).toContain('Claude、Images')
    await w.get('[data-testid="share-load-factor-input-1"]').setValue('50')
    api.getPolicy.mockClear()
    await w.get('[data-testid="share-load-factor-input-1"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(accounts.update).toHaveBeenCalledWith(1, { load_factor: 50 })
    expect(api.getPolicy).toHaveBeenCalledWith(6)
  })

  it('clears a load factor with an empty value and rejects out-of-range input', async () => {
    const w = mountView()
    await flushPromises()
    accounts.update.mockResolvedValue({})
    await w.get('[data-testid="share-load-factor-value-1"]').trigger('click')
    await w.get('[data-testid="share-load-factor-input-1"]').setValue('')
    await w.get('[data-testid="share-load-factor-input-1"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(accounts.update).toHaveBeenCalledWith(1, { load_factor: 0 })

    accounts.update.mockClear()
    await w.get('[data-testid="share-load-factor-value-1"]').trigger('click')
    await w.get('[data-testid="share-load-factor-input-1"]').setValue('20000')
    await w.get('[data-testid="share-load-factor-input-1"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(accounts.update).not.toHaveBeenCalled()
  })

  it('confirms the main switch before saving it', async () => {
    const w = mountView()
    await flushPromises()
    await w.get('[data-testid="share-global-toggle"]').trigger('click')
    expect(w.text()).toContain('admin.shareScheduling.global.offTitle')
    expect(api.updateSettings).not.toHaveBeenCalled()
    await w.get('[data-testid="share-global-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith({ enabled: false })
    expect(w.get('[data-testid="share-not-effective"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-attention"]').text()).toContain('admin.shareScheduling.risks.global_switch_off')
  })

  it('simulates a new session from the troubleshooting tools', async () => {
    api.explain.mockResolvedValue({
      group_id: 6, platform: 'openai', scheduler: 'share', version: 3, simulated_new_session: true, key_source: 'key_none',
      idle_ms: 0, cache_ttl_seconds: 1800, estimated_tokens: 0, cacheable: false, warm: false, wait_budget_ms: 0,
      reason: 'new_session', steps: [1, 2], write: 'if_absent', spill_write: 'replace', active_priority: 1,
      entries: [
        { account_id: 1, priority: 1, load_factor: 30, eligible: true, excluded: false, rank: 1, load_rate: 20, saturated: false },
        { account_id: 2, priority: 1, load_factor: 10, eligible: true, excluded: false, rank: 2, load_rate: 100, saturated: true }
      ]
    })
    const w = mountView()
    await flushPromises()
    await w.get('[data-testid="share-explain-simulate"]').trigger('click')
    await flushPromises()
    expect(api.explain).toHaveBeenCalledWith(6, { model: '' })
    const summary = w.get('[data-testid="share-explain-summary"]').text()
    expect(summary).toContain('admin.shareScheduling.explain.summary.random')
    expect(summary).toContain('"account":"alpha #1"')
    expect(summary).toContain('"list":"beta #2"')
    expect(w.get('[data-testid="share-explain-result"]').text()).toContain('admin.shareScheduling.explain.saturated')
  })
})
