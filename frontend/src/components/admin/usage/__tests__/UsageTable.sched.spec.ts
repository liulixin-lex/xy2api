import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageTable from '../UsageTable.vue'

const api = vi.hoisted(() => ({ listAttempts: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ default: api }))
vi.mock('@/utils/ipGeoLookup', () => ({ getEntry: vi.fn(() => ({ status: 'idle' })), fetchOne: vi.fn(), fetchBatch: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true })
}))

// Renders the scheduling cells and the detail slot of each row.
const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.gateway_request_id" data-test="row">
        <slot name="cell-created_at" :row="row" :value="row.created_at" />
        <slot name="cell-result" :row="row" />
        <slot name="cell-first_token" :row="row" />
        <slot name="cell-cache" :row="row" />
        <div data-test="detail"><slot name="row-detail" :row="row" /></div>
      </div>
    </div>
  `
}

const row = (patch: Record<string, unknown> = {}) => ({
  id: 1, source: 'usage', has_attempts: true, gateway_request_id: 'gw-1', created_at: '2026-11-11T12:00:00Z', result: 2, switches: 1, fallback: false, takeover: false,
  ttfo_over: false, first_token_ms: 4100, duration_ms: 19800, input_tokens: 800, cache_read_tokens: 3000, cache_creation_tokens: 200,
  ...patch
})
const mountTable = (data: unknown[], admin = true) =>
  mount(UsageTable, { props: { data, columns: [], admin } as never, global: { stubs: { DataTable: DataTableStub, Icon: true } } })

beforeEach(() => {
  api.listAttempts.mockReset().mockResolvedValue([
    { attempt_id: 'a1', account_id: 9, account_name: 'acc-9', started_at: '2026-11-11T12:00:00Z', ttfo_ms: null, duration_ms: 900, outcome: 3, error_class: 'CAPACITY_SHED' },
    { attempt_id: 'a2', account_id: 2, account_name: 'acc-2', started_at: '2026-11-11T12:00:01Z', ttfo_ms: 3200, duration_ms: 18900, outcome: 1, error_class: null }
  ])
})

describe('UsageTable — request results (FINAL §6)', () => {
  it('gives the admin page short tips and leaves the full ones to the user page', () => {
    const cost = (admin: boolean) => mount(UsageTable, {
      props: { data: [row({ long_context_billing_applied: true, actual_cost: 0.1 })], columns: [], admin } as never,
      global: { stubs: { DataTable: { props: ['data'], template: '<div><div v-for="r in data" :key="r.id"><slot name="cell-cost" :row="r" /></div></div>' }, Icon: true } }
    }).get('[data-testid="long-context-billing-marker"]').attributes('title')
    expect(cost(true)).toBe('admin.schedQuality.usage.tip.longContext')
    expect(cost(false)).toBe('admin.usage.longContextPricingTooltip')
  })

  it('shows a dash for the unknown tokens and cost of a request without a bill, and no billing tooltips', () => {
    const w = mount(UsageTable, {
      props: { data: [row({ source: 'unbilled', input_tokens: null, output_tokens: null, actual_cost: null, total_cost: null })], columns: [] } as never,
      global: { stubs: { DataTable: { props: ['data'], template: '<div><div v-for="r in data" :key="r.id"><slot name="cell-tokens" :row="r" /><slot name="cell-cost" :row="r" /></div></div>' }, Icon: true } }
    })
    expect(w.text()).toBe('——')
    expect(w.find('.group.relative').exists()).toBe(false)
  })

  it('shows the result with 兜底, 接管 and 换号 n 次 marks only when they apply', () => {
    const w = mountTable([row(), row({ gateway_request_id: 'gw-2', result: 1, switches: 0, fallback: true, takeover: true })])
    const [rescued, success] = w.findAll('[data-usage-result]')
    expect(rescued.findAll('[data-usage-mark]').map((el) => el.attributes('data-usage-mark'))).toEqual(['switches'])
    expect(rescued.get('[data-usage-mark="switches"]').text()).toBe('admin.schedQuality.usage.switches:{"n":1}')
    expect(success.findAll('[data-usage-mark]').map((el) => el.attributes('data-usage-mark'))).toEqual(['fallback', 'takeover'])
  })

  it('makes the first output yellow only when it is above the group line (ttfo_over), and shows the request cache', () => {
    const w = mountTable([row(), row({ gateway_request_id: 'gw-2', ttfo_over: true }), row({ gateway_request_id: 'gw-3', ttfo_over: null, first_token_ms: null })])
    const firsts = w.findAll('[data-test="row"]').map((r) => r.findAll('span').find((el) => el.classes().includes('sq-num'))!)
    expect(firsts[0].classes()).not.toContain('text-amber-600')
    expect(firsts[1].classes()).toContain('text-amber-600')
    expect(firsts[2].text()).toBe('—')
    expect(w.text()).toContain('75%')
  })

  it('opens a row to list its attempts, closes it again, and has no toggle where rows cannot open', async () => {
    const w = mountTable([row()])
    const toggle = w.get('[data-usage-expand]')
    await toggle.trigger('click')
    await flushPromises()
    expect(api.listAttempts).toHaveBeenCalledWith('gw-1', '2026-11-11T12:00:00Z')
    const attempts = w.findAll('[data-usage-attempt]')
    expect(attempts.map((tr) => tr.findAll('td').map((td) => td.text()))).toEqual([
      ['1', 'acc-9', 'admin.schedQuality.usage.outcome.failed', 'admin.schedQuality.usage.errorClass.CAPACITY_SHED', '—', '0.9s'],
      ['2', 'acc-2', 'admin.schedQuality.usage.outcome.success', '—', '3.2s', '18.9s']
    ])
    await toggle.trigger('click')
    expect(w.find('[data-usage-attempts]').exists()).toBe(false)
    expect(api.listAttempts).toHaveBeenCalledTimes(1)

    expect(mountTable([row()], false).find('[data-usage-expand]').exists()).toBe(false)
    expect(mountTable([row({ gateway_request_id: null })]).find('[data-usage-expand]').exists()).toBe(false)
    // Only a request with rows in the attempt table opens (RG-009): an OpenAI image request has a gateway id but none.
    expect(mountTable([row({ has_attempts: false, billing_mode: 'image' })]).find('[data-usage-expand]').exists()).toBe(false)
  })
})
