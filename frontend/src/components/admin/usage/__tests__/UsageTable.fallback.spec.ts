const ipGeoMocks = vi.hoisted(() => ({
  getEntry: vi.fn(() => ({ status: 'idle' as const })),
  fetchOne: vi.fn(),
  fetchBatch: vi.fn(),
}))

vi.mock('@/utils/ipGeoLookup', () => ipGeoMocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    }),
  }
})

import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import zh from '@/i18n/locales/zh/admin/resilience'
import en from '@/i18n/locales/en/admin/resilience'
import UsageTable from '../UsageTable.vue'

// Availability fallback is internal routing: only the admin usage page shows
// "fallback: origin → serving group"; users see their own group only.
const DataTableGroupStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id" :data-request-id="row.request_id">
        <slot name="cell-group" :row="row" />
      </div>
    </div>
  `,
}

const baseRow = {
  model: 'gpt-6',
  actual_cost: 0.1,
  total_cost: 0.1,
  rate_multiplier: 1,
  input_tokens: 1,
  output_tokens: 1,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  cache_ttl_overridden: false,
}

const rows = [
  { ...baseRow, request_id: 'req-fallback', group_id: 8, group: { id: 8, name: 'Backup' }, origin_group_id: 3, origin_group_name: 'Main' },
  { ...baseRow, request_id: 'req-plain', group_id: 3, group: { id: 3, name: 'Main' } },
]

function mountTable(props: Record<string, unknown> = {}) {
  return mount(UsageTable, {
    props: { data: rows, loading: false, columns: [], ...props },
    global: { stubs: { DataTable: DataTableGroupStub, EmptyState: true, Icon: true, Teleport: true } },
  })
}

describe('UsageTable availability fallback marker', () => {
  it('shows "fallback: origin → serving group" when the admin page asks for it', () => {
    const wrapper = mountTable({ showFallbackOrigin: true })
    const markers = wrapper.findAll('[data-testid="usage-fallback-marker"]')
    expect(markers).toHaveLength(1)
    const marker = wrapper.get('[data-request-id="req-fallback"]').get('[data-testid="usage-fallback-marker"]')
    expect(marker.text()).toBe('admin.resilience.usage.fallback:{"origin":"Main","group":"Backup"}')
    expect(marker.attributes('title')).toBe('admin.resilience.usage.fallbackTitle')
  })

  it('never shows the fallback to users, even if an older server still sends the origin fields', () => {
    const wrapper = mountTable()
    expect(wrapper.find('[data-testid="usage-fallback-marker"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.resilience.usage.fallback')
    expect(wrapper.get('[data-request-id="req-plain"]').text()).toBe('Main')
  })

  it.each([['zh', zh, '原分组'], ['en', en, 'original group']])('%s explains that the original group is billed', (_, messages, billed) => {
    const title = messages.resilience.usage.fallbackTitle
    expect(title).toContain(billed)
    expect(title).not.toMatch(/按其倍率|at its rate/)
  })
})
