import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsResilienceCard from '../OpsResilienceCard.vue'

const mocks = vi.hoisted(() => ({ stats: vi.fn(), list: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))
vi.mock('@/utils/format', () => ({ formatNumber: (n: number) => String(n), formatDateTime: (d: string) => d, formatCostFixed: (n: number) => n.toFixed(6) }))
vi.mock('@/api/admin', () => ({ adminAPI: { resilience: { stats: mocks.stats, listUndelivered: mocks.list } } }))

function mountCard(refreshToken = 0) {
  return mount(OpsResilienceCard, { props: { refreshToken }, global: { stubs: { Select: true, OpsResilienceExplain: true } } })
}

describe('ops resilience card', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.stats.mockResolvedValue({
      window_seconds: 3600,
      scope: 'instance',
      counters: [
        { name: 'keepalive_committed', group_id: 3, label: 'responses', count: 4 },
        { name: 'keepalive_committed', group_id: 5, label: 'chat', count: 2 },
        { name: 'undelivered', group_id: 3, label: 'pre_output', count: 1 }
      ],
      balance_states: { OK: 7, EXHAUSTED: 1 }
    })
    mocks.list.mockResolvedValue({
      items: [{
        id: 9, request_id: 'r1', client_request_id: '', user_id: 2, api_key_id: 4, group_id: 3, platform: 'openai',
        model: 'gpt-5.5', inbound_endpoint: '/v1/responses', disconnect_stage: 'pre_output', duration_ms: 125400,
        estimated_upstream_cost: 0, billed: false, created_at: '2026-10-15T08:00:00Z'
      }],
      total: 1, page: 1, page_size: 20, pages: 1
    })
  })

  it('sums counters per metric and lists undelivered requests', async () => {
    const w = mountCard(); await flushPromises()
    expect(mocks.stats).toHaveBeenCalledWith('1h')
    expect(mocks.list).toHaveBeenCalledWith({ page: 1, page_size: 20 })
    const totals = w.get('[data-testid="resilience-totals"]').text()
    expect(totals).toContain('keepalive_committed')
    expect(totals).toContain('6')
    expect(w.get('[data-testid="resilience-balance-states"]').text()).toContain('1')
    const table = w.get('[data-testid="resilience-undelivered-table"]').text()
    expect(table).toContain('/v1/responses')
    expect(table).toContain('125.4s')
    expect(table).toContain('admin.resilience.undelivered.notBilled')
  })

  it('reloads when the dashboard refreshes and shows empty states', async () => {
    const w = mountCard(); await flushPromises()
    mocks.stats.mockResolvedValueOnce({ window_seconds: 3600, scope: 'instance', counters: [], balance_states: {} })
    mocks.list.mockResolvedValueOnce({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    await w.setProps({ refreshToken: 1 }); await flushPromises()
    expect(mocks.stats).toHaveBeenCalledTimes(2)
    expect(w.find('[data-testid="resilience-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="resilience-undelivered-empty"]').exists()).toBe(true)
  })

  it('reports a failed stats request', async () => {
    mocks.stats.mockRejectedValueOnce({ status: 500, message: 'stats down' })
    const w = mountCard(); await flushPromises()
    expect(w.text()).toContain('stats down')
  })
})
