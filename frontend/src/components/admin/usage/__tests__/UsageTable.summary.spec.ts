// Mobile list plan, usage records: tokens, cost and latency on one line each in every width, everything else in a
// per-row request detail dialog (click, Enter or Space) that shows only what the page may show.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageTable from '../UsageTable.vue'

vi.mock('@/utils/ipGeoLookup', () => ({ getEntry: vi.fn(() => ({ status: 'idle' })), fetchOne: vi.fn(), fetchBatch: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const thisYear = new Date().getFullYear()
const createdAt = new Date(thisYear, 10, 20, 6, 54, 11).toISOString()

const row = {
  id: 9, source: 'usage', result: 1, switches: 0, request_id: 'r-1', created_at: createdAt,
  user_id: 12, user: { id: 12, email: 'alice@example.com', deleted_at: '2026-01-01T00:00:00Z' }, api_key: { id: 3, name: 'prod-key' },
  model: 'gpt-6.1-sol', upstream_model: 'gpt-6.1', account: { id: 1, name: 'acc-1' },
  input_tokens: 12345, output_tokens: 678, cache_read_tokens: 1500, cache_creation_tokens: 40,
  cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0, cache_ttl_overridden: false,
  input_cost: 0.01, output_cost: 0.002, cache_creation_cost: 0, cache_read_cost: 0.000345,
  actual_cost: 1234.012345, total_cost: 0.012345, rate_multiplier: 1, account_rate_multiplier: 0.5, account_stats_cost: 0.000474,
  billing_mode: 'token', image_count: 0, duration_ms: 3000, first_token_ms: 900, end_to_end_first_output_ms: 1200,
  inbound_endpoint: '/v1/messages', upstream_endpoint: '/v1/responses',
  group_id: 8, group: { id: 8, name: 'Backup' }, origin_group_id: 3, origin_group_name: 'Main',
}

const CELLS = ['user', 'api_key', 'account', 'model', 'endpoint', 'group', 'tokens', 'cost', 'latency', 'created_at', 'actions']
const DataTableStub = {
  props: ['data', 'layout'],
  template: `<div><div v-for="row in data" :key="row.id" :data-row="row.id">
    ${CELLS.map((key) => `<div data-cell="${key}"><slot name="cell-${key}" :row="row" :value="row.${key}" /></div>`).join('')}
  </div></div>`
}

const ADMIN = { showEndToEnd: true, showFallbackOrigin: true, showTokenSpeed: true, showCacheHitRate: true }
const USER = { showAccountBilling: false, showUpstreamEndpoint: false, showTokenSpeed: true, showCacheHitRate: true }

function mountTable(props: Record<string, unknown>, data: unknown[] = [row]) {
  return mount(UsageTable, {
    props: { data: data as never, columns: [], ...props },
    global: { stubs: { DataTable: DataTableStub, Icon: true, Teleport: true } },
    attachTo: document.body,
  })
}

const cell = (w: ReturnType<typeof mountTable>, key: string) => w.get(`[data-cell="${key}"]`)

afterEach(() => { document.body.innerHTML = '' })

describe('UsageTable one-line summaries', () => {
  it('keeps tokens to input and output, with the cache counts left to the tip and the detail', () => {
    const tokens = cell(mountTable(ADMIN), 'tokens')
    expect(tokens.get('[data-testid="usage-token-summary"]').text()).toBe('12,345678')
    expect(tokens.text()).not.toContain('1,500')
    expect(tokens.find('[data-testid="usage-cache-hit-rate"]').exists()).toBe(false)
  })

  it('writes the cost with six decimals and thousands separators, the account cost "A $…" on the same line', () => {
    const admin = cell(mountTable(ADMIN), 'cost')
    const line = admin.get('.flex')
    expect(line.text()).toContain('$1,234.012345')
    expect(line.get('[data-testid="usage-account-billed"]').text()).toBe('A $0.000237')
    expect(line.get('[data-testid="usage-account-billed"]').classes()).toContain('text-xs')
    const user = cell(mountTable(USER), 'cost')
    expect(user.find('[data-testid="usage-account-billed"]').exists()).toBe(false)
    expect(user.text()).not.toContain('A $')
  })

  it('puts the requested and the sent model on one line, and the endpoints only where the page shows the upstream one', () => {
    const admin = mountTable(ADMIN)
    expect(cell(admin, 'model').get('[data-testid="usage-model-steps"]').findAll('span').map((s) => s.text())).toEqual(['gpt-6.1-sol', '→', 'gpt-6.1'])
    const endpoint = cell(admin, 'endpoint').get('[data-testid="usage-endpoint"]')
    expect(endpoint.findAll('span').map((s) => s.text())).toEqual(['/v1/messages', '→', '/v1/responses'])
    expect(endpoint.attributes('title')).toBe('usage.inboundEndpoint: /v1/messages\nusage.upstreamEndpoint: /v1/responses')
    const user = cell(mountTable(USER), 'endpoint').get('[data-testid="usage-endpoint"]')
    expect(user.text()).toBe('/v1/messages')
    expect(user.attributes('title')).toBe('usage.inboundEndpoint: /v1/messages')
  })

  it('keeps the account and its rescue marks, and the group and its fallback, on one line', () => {
    const wrapper = mountTable(ADMIN, [{ ...row, result: 2, switches: 1 }])
    const account = cell(wrapper, 'account')
    expect(account.get('[data-testid="usage-request-marks"]').classes()).toContain('inline-flex')
    expect(account.text()).toBe('acc-1admin.usage.resultRescuedadmin.usage.switches:{"n":1}')
    expect(cell(wrapper, 'group').get('div').classes()).toEqual(expect.arrayContaining(['flex', 'items-center']))
    expect(cell(wrapper, 'group').text()).toContain('Main')
  })

  it('shows this year\'s time without the year, next to the row\'s Details button', () => {
    const time = cell(mountTable(ADMIN), 'created_at')
    expect(time.get('span').text()).toBe('11-20 06:54:11')
    expect(time.get('button').text()).toBe('usage.requestDetail.button')
  })

  // The shared empty cell (mobile list plan, copy): one light "—".
  it('shows the shared light dash for an empty account, endpoint and group', () => {
    const wrapper = mountTable(USER, [{ ...row, account: null, inbound_endpoint: '', group: null, group_id: null, origin_group_id: null }])
    for (const key of ['account', 'endpoint', 'group']) {
      expect(cell(wrapper, key).get('[data-test="cell-empty"]').text()).toBe('—')
    }
  })
})

describe('UsageTable request detail', () => {
  it('opens from a named native button and closes with Escape', async () => {
    const wrapper = mountTable(ADMIN)
    const button = cell(wrapper, 'created_at').get('button')
    // A native button: Enter and Space press it like a click, and it stays in the Tab order.
    expect(button.attributes('type')).toBe('button')
    expect(button.attributes('tabindex')).toBeUndefined()
    expect(button.attributes('aria-haspopup')).toBe('dialog')
    expect(button.attributes('aria-label')).toBe('usage.requestDetail.open:{"time":"11-20 06:54:11"}')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)

    await button.trigger('click')
    await nextTick()
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.attributes('aria-modal')).toBe('true')
    expect(wrapper.get(`#${dialog.attributes('aria-labelledby')}`).text()).toBe('usage.requestDetail.title')
    expect(dialog.get('[data-testid="usage-request-detail-time"]').text()).toBe(`${thisYear}-11-20 06:54:11`)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('lists the admin page\'s full breakdown: user wait, last attempt, account billing, upstream endpoint, fallback', async () => {
    const wrapper = mountTable(ADMIN)
    await cell(wrapper, 'created_at').get('button').trigger('click')
    const detail = wrapper.get('[data-testid="usage-request-detail"]')
    expect(detail.get('[data-testid="usage-detail-e2e"]').text()).toBe('1.20s')
    expect(detail.get('[data-testid="usage-detail-first-token"]').text()).toBe('900ms')
    expect(detail.get('[data-testid="usage-detail-duration"]').text()).toBe('3.00s')
    expect(detail.get('[data-testid="usage-token-speed"]').text()).toBe('226.0 t/s')
    expect(detail.text()).toContain('admin.usage.cacheReadTokens')
    expect(detail.text()).toContain('1,500')
    expect(detail.get('[data-testid="usage-cache-hit-rate"]').text()).toBe('10.8%')
    expect(detail.text()).toContain('usage.accountBilled')
    expect(detail.text()).toContain('$0.00023700')
    expect(detail.get('[data-testid="usage-detail-upstream-endpoint"]').text()).toContain('/v1/responses')
    expect(detail.get('[data-testid="usage-detail-fallback"]').text()).toBe('admin.resilience.usage.fallback:{"group":"Backup"}')
    expect(detail.get('[data-testid="usage-detail-model"]').text()).toBe('gpt-6.1-sol → gpt-6.1')
  })

  it('projects the user page: no account billing, upstream endpoint, fallback or user wait, even if the answer has them', async () => {
    const wrapper = mountTable(USER)
    await cell(wrapper, 'created_at').get('button').trigger('click')
    const detail = wrapper.get('[data-testid="usage-request-detail"]')
    expect(detail.text()).not.toContain('usage.accountBilled')
    expect(detail.text()).not.toContain('usage.accountMultiplier')
    expect(detail.find('[data-testid="usage-detail-upstream-endpoint"]').exists()).toBe(false)
    expect(detail.text()).not.toContain('/v1/responses')
    expect(detail.find('[data-testid="usage-detail-fallback"]').exists()).toBe(false)
    expect(detail.text()).not.toContain('admin.resilience.usage.fallback')
    expect(detail.text()).not.toContain('Main')
    expect(detail.find('[data-testid="usage-detail-e2e"]').exists()).toBe(false)
    expect(detail.text()).not.toContain('1.20s')
    expect(detail.text()).toContain('usage.userBilled')
    expect(detail.text()).toContain('usage.inboundEndpoint')
  })

  it('says a request without a bill was not billed, with its result, and invents no token counts', async () => {
    const unbilled = { ...row, id: 4, source: 'unbilled', result: 3, actual_cost: null, total_cost: null, input_tokens: null, output_tokens: null }
    const wrapper = mountTable(ADMIN, [unbilled])
    await cell(wrapper, 'created_at').get('button').trigger('click')
    const detail = wrapper.get('[data-testid="usage-request-detail"]')
    expect(detail.get('[data-testid="usage-detail-unbilled"]').text().replace(/\s+/g, ' ')).toBe('— usage.requestDetail.unbilled · admin.usage.resultFailed')
    expect(detail.text()).not.toContain('usage.userBilled')
    expect(detail.text()).not.toContain('admin.usage.inputTokens')
  })
})

describe('UsageTable on the shared table layout (B)', () => {
  const setPhone = (phone: boolean) => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: query.includes('min-width') ? !phone : true, media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) as unknown as MediaQueryList)
  afterEach(() => { vi.restoreAllMocks() })

  it('asks for the table at every width', () => {
    expect(mountTable(ADMIN).findComponent(DataTableStub).props('layout')).toBe('table')
  })

  // Owner ruling: one line on a phone (rows near 53px, about ten a screen); the id and the deleted mark are in the detail.
  // Review: the email is a 40px-high target (a pseudo-element), and the detail opens from the frozen right column.
  it('makes the admin page\'s first column a one-line identity cell on a phone, the email still opening the balance', async () => {
    setPhone(true)
    const wrapper = mountTable({ ...ADMIN, columns: [{ key: 'user', label: 'User' }, { key: 'account', label: 'Account' }, { key: 'created_at', label: 'Time' }] })
    const email = cell(wrapper, 'user').get('[data-testid="usage-identity-user"]')
    expect(email.text()).toBe('alice@example.com')
    expect(email.classes()).toEqual(expect.arrayContaining(['relative', 'before:absolute', 'before:-inset-y-3']))
    expect(email.get('span').classes()).toContain('truncate')
    expect(cell(wrapper, 'user').text()).not.toContain('#12')
    await email.trigger('click')
    expect(wrapper.emitted('userClick')).toEqual([[12, 'alice@example.com']])
    // Only the frozen first column: the API key is a plain cell here.
    expect(cell(wrapper, 'api_key').find('[data-test="table-identity"]').exists()).toBe(false)
    // What the second line carried is in the row's detail.
    await cell(wrapper, 'actions').get('button').trigger('click')
    const user = wrapper.get('[data-testid="usage-detail-user"]')
    expect(user.get('dt').text()).toBe('admin.usage.user')
    expect(user.findAll('dd > span').map((el) => el.text())).toEqual(['alice@example.com', '#12', '· admin.usage.userDeletedBadge'])
  })

  it('names no user in the user page\'s detail', async () => {
    const wrapper = mountTable({ ...USER, columns: [{ key: 'api_key', label: 'Key' }, { key: 'created_at', label: 'Time' }] })
    await cell(wrapper, 'created_at').get('button').trigger('click')
    expect(wrapper.find('[data-testid="usage-detail-user"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="usage-request-detail"]').text()).not.toContain('alice@example.com')
  })

  it('makes the user page\'s API key the identity cell on a phone, and keeps the one-line cells from 768px', () => {
    setPhone(true)
    const phone = mountTable({ ...USER, columns: [{ key: 'api_key', label: 'Key' }, { key: 'model', label: 'Model' }] })
    expect(cell(phone, 'api_key').get('[data-test="table-identity"]').text()).toBe('prod-key')
    vi.restoreAllMocks()
    setPhone(false)
    const wide = mountTable({ ...ADMIN, columns: [{ key: 'user', label: 'User' }] })
    expect(cell(wide, 'user').find('[data-test="table-identity"]').exists()).toBe(false)
    expect(cell(wide, 'user').text()).toContain('alice@example.com')
  })
})

// Review fixes (mobile list plan, step 1 review): items 3, 5, 8 and the adopted suggestions on the detail.
describe('UsageTable after review', () => {
  const setWidth = (width: number) => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
    const min = /min-width:\s*(\d+)px/.exec(query)
    return {
      matches: min ? width >= Number(min[1]) : true, media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList
  })
  afterEach(() => { vi.restoreAllMocks() })

  it.each([390, 834])('puts "Details" in a frozen right column below 1024px (%ipx), not beside the time', async (width) => {
    setWidth(width)
    const wrapper = mountTable(ADMIN)
    expect(cell(wrapper, 'created_at').find('button').exists()).toBe(false)
    const button = cell(wrapper, 'actions').get('[data-testid="usage-detail-open"]')
    expect(button.attributes('aria-label')).toBe('usage.requestDetail.open:{"time":"11-20 06:54:11"}')
    expect(button.classes()).toEqual(expect.arrayContaining(['h-10', 'w-10']))
    await button.trigger('click')
    expect(wrapper.find('[data-testid="usage-request-detail"]').exists()).toBe(true)
  })

  it('names the actions column "Details" below 1024px and adds none from 1024px', () => {
    const ColumnsStub = { props: ['data', 'columns', 'layout'], template: '<div />' }
    const mountWith = () => mount(UsageTable, {
      props: { data: [row] as never, ...ADMIN, columns: [{ key: 'user', label: 'User' }] },
      global: { stubs: { DataTable: ColumnsStub, Icon: true, Teleport: true } },
    })
    setWidth(834)
    expect(mountWith().findComponent(ColumnsStub).props('columns').map((c: { key: string }) => c.key)).toEqual(['user', 'actions'])
    vi.restoreAllMocks()
    setWidth(1280)
    expect(mountWith().findComponent(ColumnsStub).props('columns').map((c: { key: string }) => c.key)).toEqual(['user'])
  })

  it('flips a hover tip to the left of its icon, and keeps it inside the viewport, when it would leave on the right', async () => {
    const wrapper = mountTable(ADMIN)
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1100)
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(600)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(200)
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(120)
    const trigger = cell(wrapper, 'latency').get('[data-testid="usage-latency-tip"]')
    vi.spyOn(trigger.element, 'getBoundingClientRect').mockReturnValue({ left: 1070, right: 1086, top: 580, bottom: 596, width: 16, height: 16, x: 1070, y: 580, toJSON: () => ({}) } as DOMRect)
    await trigger.trigger('mouseenter')
    await nextTick()
    await nextTick()
    const tip = wrapper.get('.fixed')
    expect(tip.attributes('style')).toContain('left: 862px') // 1070 - 8 - 200
    expect(tip.attributes('style')).toContain('top: 532px') // 600 - 8 - 120 / 2
    expect(tip.find('.left-full').exists()).toBe(true)
    expect(tip.find('.right-full').exists()).toBe(false)
  })

  it('hides the hover-only info icons on touch screens, where "Details" carries them', () => {
    const wrapper = mountTable(ADMIN)
    for (const key of ['tokens', 'cost']) expect(cell(wrapper, key).get('.group.relative').classes()).toContain('[@media(hover:none)]:hidden')
    expect(cell(wrapper, 'latency').get('[data-testid="usage-latency-tip"]').classes()).toContain('[@media(hover:none)]:hidden')
  })

})
