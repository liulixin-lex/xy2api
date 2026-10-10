import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ShareSchedulingView from '../ShareSchedulingView.vue'
import { shareMember, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  listGroups: vi.fn(),
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  preview: vi.fn(),
  explain: vi.fn(),
  report: vi.fn(),
  decisions: vi.fn(),
  rebalance: vi.fn()
}))
const router = vi.hoisted(() => ({ replace: vi.fn(), leave: [] as Array<(to: { path: string }) => unknown>, query: {} as Record<string, string> }))

vi.mock('@/api/admin/shareScheduling', () => ({ default: api }))
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
  onBeforeRouteLeave: (guard: (to: { path: string }) => unknown) => { router.leave.push(guard) },
  RouterLink: { props: ['to'], template: '<a :href="String(to)"><slot /></a>' }
}))

const groups = [
  { group_id: 5, name: 'Claude', platform: 'anthropic', scheduler: 'sub2api', version: 1, configured: true, effective: false, requests_1h: 12, cache_read_ratio_1h: 0.8, scheduling_miss_ratio_1h: 0.01 },
  { group_id: 6, name: 'Codex', platform: 'openai', scheduler: 'shadow', version: 2, configured: true, effective: true, requests_1h: 0, cache_read_ratio_1h: 0, scheduling_miss_ratio_1h: 0, shadow_agree_rate_1h: 0.9 }
]

function mountView() {
  return mount(ShareSchedulingView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-dialog><slot /><slot name="footer" /></div>' },
        HelpTooltip: true,
        Pagination: true
      }
    }
  })
}

async function openGroup(w: ReturnType<typeof mountView>, id = 5) {
  await flushPromises()
  await w.get(`[data-testid="share-group-${id}"]`).trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  router.leave = []
  router.query = {}
  api.getSettings.mockResolvedValue({ enabled: false, cache_attribution: true, attribution_sample_rate: 4, decision_samples: true })
  api.listGroups.mockResolvedValue(groups)
  api.getPolicy.mockImplementation(async (id: number) => shareView(id, {
    members: [shareMember(1, { tier: 1, weight: 10 }), shareMember(2, { tier: 1, weight: 10, healthy: false, reason: 'rate_limited', recover_at: '2026-10-10T10:00:00Z' })]
  }))
  api.preview.mockResolvedValue({ active_tier: 1, members: [shareMember(1, { effective_share: 0.75 })], placement_change: 0.25, sampled_keys: 400, estimated_migrations: 0, warnings: [{ code: 'global_switch_off' }] })
  api.report.mockResolvedValue({
    group_id: 5, scheduler: 'share', version: 1, unit: 'sessions', from: '', to: '', active_tier: 1,
    members: [shareMember(1, { effective_share: 0.5 }), shareMember(2, { effective_share: 0.5 })],
    actual: [{ account_id: 1, placed: 6, placed_share: 0.6, requests: 0, request_share: 0, tokens: 0, token_share: 0, standard_cost: 0, cost_share: 0, skipped: { saturated: 3, session_limit: 1 }, ttft: { count: 5, p50_ms: 500, p95_ms: 2000, p99_ms: 3000 } }],
    decisions: { requests: 10, reasons: { affinity_hit: 7, new_session: 3 }, key_sources: {}, ideal_skips: {}, hot_migrations: 0, hot_reasons: {}, wait_count: 0, wait_hit: 0, avg_wait_ms: 0, attempts: 10, shadow: 0, shadow_agree: 0, shadow_agree_rate: 0, shadow_reasons: {}, rebalance_cleared: 0, dropped: 0 },
    cache: { requests: 4, classes: {}, class_tokens: { cache_hit: 900, miss_new_session: 100 }, prompt_tokens: 1000, cache_read_tokens: 900, cache_creation_tokens: 0, read_ratio: 0.9, scheduling_miss_ratio: 0, ttft: { affinity_hit: { count: 4, p50_ms: 500, p95_ms: 1000, p99_ms: null } }, dropped: 0 }
  })
  api.decisions.mockResolvedValue([{ at: '2026-10-10T08:00:00Z', version: 1, scheduler: 'share', key_source: 'key_session', reason: 'affinity_hit', bound: 1, served: 1, attempts: 1, elapsed_us: 30 }])
})

describe('share scheduling page', () => {
  it('lists groups, warns about the master switch and shows the live member state', async () => {
    const w = mountView()
    await openGroup(w)
    expect(w.get('[data-testid="share-global-off"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-group-title"]').text()).toBe('group-5')
    expect(router.replace).toHaveBeenCalledWith({ query: { group: '5' } })
    expect(w.get('[data-testid="share-row-2"]').text()).toContain('admin.shareScheduling.reasons.rate_limited')
    expect(w.get('[data-testid="share-active-tier"]').text()).toContain('"tier":1')
    expect(w.get('[data-testid="share-shares-1"]').text()).toContain('100.0%')
  })

  it('previews before saving and keeps the draft on a version conflict', async () => {
    const w = mountView()
    await openGroup(w)
    await w.get('[data-testid="share-weight-1"]').setValue('30')
    await w.get('[data-testid="share-scheduler-share"]').setValue(true)
    expect(w.get('[data-testid="share-draft-status"]').text()).toBe('admin.shareScheduling.policy.dirty')
    expect(w.text()).toContain('admin.shareScheduling.groups.draft')

    await w.get('[data-testid="share-preview-save"]').trigger('click')
    await flushPromises()
    expect(api.preview).toHaveBeenCalledWith(5, expect.objectContaining({ scheduler: 'share', accounts: [{ account_id: 1, tier: 1, weight: 30 }] }))
    expect(w.get('[data-testid="share-preview-placement"]').text()).toBe('25.0%')
    expect(w.get('[data-testid="share-preview-warnings"]').text()).toContain('warnings.global_switch_off')

    api.updatePolicy.mockRejectedValue({ status: 409, reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })
    api.getPolicy.mockResolvedValueOnce(shareView(5, { version: 4 }))
    await w.get('[data-testid="share-preview-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updatePolicy).toHaveBeenCalledWith(5, 1, expect.anything())
    expect(w.get('[data-testid="share-conflict"]').text()).toContain('"version":4')
    expect((w.get('[data-testid="share-weight-1"]').element as HTMLInputElement).value).toBe('30')
    expect(w.get('[data-testid="share-preview-save"]').attributes('disabled')).toBeDefined()

    api.getPolicy.mockResolvedValueOnce(shareView(5, { version: 4 }))
    await w.get('[data-testid="share-rebase"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="share-conflict"]').exists()).toBe(false)
    expect((w.get('[data-testid="share-weight-1"]').element as HTMLInputElement).value).toBe('30')

    await w.get('[data-testid="share-preview-save"]').trigger('click')
    await flushPromises()
    api.updatePolicy.mockImplementation(async (id: number, _v: number, policy) => shareView(id, { version: 5, scheduler: 'share', policy }))
    await w.get('[data-testid="share-preview-confirm"]').trigger('click')
    await flushPromises()
    expect(api.updatePolicy).toHaveBeenLastCalledWith(5, 4, expect.anything())
    expect(w.get('[data-testid="share-saved"]').text()).toContain('"version":5')
    expect(w.get('[data-testid="share-draft-status"]').text()).toBe('admin.shareScheduling.policy.clean')
  })

  it('blocks invalid drafts locally and edits rows in batches', async () => {
    const w = mountView()
    await openGroup(w)
    const boxes = w.findAll('tbody input[type="checkbox"][value]')
    for (const box of boxes) await box.setValue(true)
    await w.get('[data-testid="share-batch-weight"]').setValue('0')
    await w.get('[data-testid="share-batch-apply"]').trigger('click')
    await w.get('[data-testid="share-preview-save"]').trigger('click')
    await flushPromises()
    expect(api.preview).not.toHaveBeenCalled()
    expect(w.get('[data-testid="share-problems"]').text()).toContain('problems.weightRange')
  })

  it('asks before leaving with unsaved drafts', async () => {
    const w = mountView()
    await openGroup(w)
    expect(await router.leave[0]({ path: '/admin/accounts' })).toBe(true)
    await w.get('[data-testid="share-weight-1"]').setValue('30')
    const leaving = router.leave[0]({ path: '/admin/accounts' }) as Promise<boolean>
    await flushPromises()
    await w.get('[data-testid="share-stay"]').trigger('click')
    expect(await leaving).toBe(false)
  })

  it('renders the report and the explanation of a group', async () => {
    const w = mountView()
    await openGroup(w)
    await w.get('[data-testid="share-tab-report"]').trigger('click')
    await flushPromises()
    expect(api.report).toHaveBeenCalledWith(5, '1h')
    expect(w.get('[data-testid="share-card-cacheRead"]').text()).toBe('90.0%')
    expect(w.get('[data-testid="share-report-row-1"]').text()).toContain('60.0%')
    expect(w.get('[data-testid="share-ttft"]').text()).toContain('> 30 s')
    const busy = w.get('[data-testid="share-account-1"]').text()
    expect(busy).toContain('40.0%')
    expect(busy).toContain('admin.shareScheduling.reasons.saturated 3 · admin.shareScheduling.reasons.session_limit 1')
    expect(busy).toContain('≤ 2.0 s')
    expect(w.get('[data-testid="share-account-2"]').text()).toContain('-')
    expect(w.find('[data-testid="share-account-busy"]').exists()).toBe(true)
    expect(w.get('[data-testid="share-decisions"]').text()).toContain('reasons.affinity_hit')

    api.explain.mockResolvedValue({
      group_id: 5, platform: 'anthropic', scheduler: 'sub2api', version: 1, key_source: 'key_session', session: 'abc', idle_ms: 0,
      cache_ttl_seconds: 300, estimated_tokens: 2000, cacheable: true, warm: false, wait_budget_ms: 0, reason: 'new_session',
      steps: [2, 1], write: 'if_absent', spill_write: 'none', target: 2, ideal: 2, active_tier: 1,
      entries: [{ account_id: 2, tier: 1, weight: 10, effective_weight: 10, configured: false, drain: false, cache_domain: 'account:2', eligible: true, excluded: false, rank: 1 }]
    })
    await w.get('[data-testid="share-tab-explain"]').trigger('click')
    await w.get('[data-testid="share-explain-body"]').setValue('{not json')
    await w.get('[data-testid="share-explain-run"]').trigger('submit')
    await flushPromises()
    expect(api.explain).not.toHaveBeenCalled()
    expect(w.text()).toContain('explain.bodyInvalid')

    await w.get('[data-testid="share-explain-body"]').setValue('')
    await w.get('[data-testid="share-explain-model"]').setValue('claude-x')
    await w.get('[data-testid="share-explain-session"]').setValue('sess')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.explain).toHaveBeenCalledWith(5, { model: 'claude-x', session_hash: 'sess', cache_one_hour: false })
    expect(w.get('[data-testid="share-explain-steps"]').text()).toContain('1. acct-2 #2')
  })
})
