import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, h } from 'vue'

import zhSched from '@/i18n/locales/zh/admin/schedQuality'
import type { SchedAccountDetail, SchedAccountSummary, SchedGroupSummary } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import SchedGroupsCell from '../SchedGroupsCell.vue'
import SchedNumCell from '../SchedNumCell.vue'
import SchedStatusCell from '../SchedStatusCell.vue'
import SchedRecoverLine from '../SchedRecoverLine.vue'
import GroupSchedCell from '@/components/admin/group/GroupSchedCell.vue'
import { GROUP_SCHED_COLUMNS } from '@/utils/schedQualityFormat'
import SchedBatchDialog from '../SchedBatchDialog.vue'
import SchedOverridePopover from '../SchedOverridePopover.vue'
import SchedPauseMark from '../SchedPauseMark.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import GroupSchedPolicyField from '@/components/admin/group/GroupSchedPolicyField.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedBoard, openSchedDrawer } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), groupOrder: vi.fn(), getGroupPolicy: vi.fn(), previewGroupPolicy: vi.fn(), reset: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => true) }) }
})
vi.mock('@/stores/adminAccess', () => ({ useAdminAccessStore: () => ({ can: () => true }) }))

// Acceptance item 3 of WP7: every text the new components render (visible text and tips) is data or comes from
// design/copy.md, and none is a sentence. Existing keys reused by the design (column "操作", close) are listed too.
const messages = { admin: { ...zhSched, accounts: { columns: { actions: '操作' } } }, common: { close: '关闭' } }
const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: messages } })

const copyTokens = new Set<string>()
const collect = (node: unknown) => {
  if (typeof node === 'string') {
    for (const token of node.replace(/\{\w+\}/g, ' ').split(/\s+/)) if (token) copyTokens.add(token)
  } else if (node && typeof node === 'object') Object.values(node).forEach(collect)
}
collect(messages)

// Data: numbers with units, times, model / account / group names from the fixtures, dashes and separators.
const DATA = [/gpt-[\w.-]+/g, /acc-[\w-]+|…|晨星组|北辰组/g, /[\d.:,%/+#×s·—→-]+/g]
function nonData(text: string) {
  let rest = text
  for (const pattern of DATA) rest = rest.replace(pattern, ' ')
  return rest.split(/\s+/).filter(Boolean)
}

// Visible text and tips; screen-reader-only descriptions are checked on their own (they may join items with the
// list separator).
function rendered(): string[] {
  const out: string[] = []
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent?.trim()
    if (text && !node.parentElement?.closest('.sr-only')) out.push(text)
  }
  document.querySelectorAll<HTMLElement & { __tipText?: string }>('*').forEach((el) => { if (el.__tipText) out.push(el.__tipText) })
  return out
}

const detail = () => {
  const d = structuredClone(detailFixture) as SchedAccountDetail
  d.auto_states.push(
    { key: 'proxy_circuit:7', kind: 'proxy_circuit', scope: 'proxy', model: '', until: null, count: [2, 3], shared: 4, clearable: true, why: '' },
    { key: 'parent_blocked:9', kind: 'parent_blocked', scope: 'account', model: '', until: null, count: null, shared: 0, clearable: false, why: 'parent' },
    { key: 'x', kind: 'not_in_table', scope: 'account', model: '', until: null, count: null, shared: 0, clearable: false, why: '' }
  )
  return d
}
const groupSummary = (patch: Partial<SchedGroupSummary> = {}): SchedGroupSummary => ({
  group_id: 53, enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0.7, takeover_idle_seconds: 900,
  accounts: { normal: 5, below: 1, stopped: 2 }, last60: { ttfo_p50_ms: 7200, failed_rate: 0.008, cache_ratio: 0.78, requests: 1942 }, ...patch
})

enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedDrawer()
  closeSchedBoard()
  document.documentElement.dataset.motion = 'reduced'
  api.accountDetail.mockResolvedValue(detail())
  api.groupOrder.mockResolvedValue(orderFixture)
  api.getGroupPolicy.mockResolvedValue(policyFixture)
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('quality UI copy', () => {
  it('renders only names, state words and value templates from the copy table', async () => {
    const summary = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
    const Host = defineComponent({
      setup: () => () => [
        h(SchedStatusCell, { summary }),
        h(SchedStatusCell, { summary: { ...summary, status: 'observing', reset_at: new Date().toISOString() } }),
        h(SchedStatusCell, { summary: { ...summary, status: 'disabled', stop: { source: 'auto', reason: 'rate_limit', until: new Date().toISOString() } } }),
        ...(['ttfo', 'error', 'cache'] as const).map((dim) => h(SchedNumCell, { summary, dim })),
        h(SchedGroupsCell, { summary, groupName: () => '晨星组' }),
        h(SchedGroupsCell, { summary: { ...summary, exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }, groupName: () => '晨星组' }),
        h(SchedRecoverLine, { line: { at: Date.now(), result: { account_id: 479, reset_at: null, cleared: ['quality', 'session_avoid'], kept: [{ kind: 'quota', until: new Date().toISOString() }], failed: [{ kind: 'rate_limit' }] } } }),
        ...GROUP_SCHED_COLUMNS.map(([col]) => h(GroupSchedCell, { col, summary: groupSummary() })),
        h(SchedPauseMark, { globalEnabled: false }),
        h(SchedPauseMark, { globalEnabled: true, stale: true }),
        h(SchedAccountDrawer),
        h(GroupSchedBoard),
        h(GroupSchedPolicyField, { groupId: 53 }),
        h(SchedOverridePopover, { show: true, anchor: null, groups: [{ id: 53, name: '晨星组' }] }),
        h(SchedBatchDialog, { action: 'reset', accounts: [{ id: 1, name: 'acc-1' }, { id: 2, name: 'acc-2' }], anchor: null, groups: [] })
      ]
    })
    mount(Host, { attachTo: document.body, global: { plugins: [i18n], stubs: { RouterLink: { template: '<a><slot /></a>' } } } })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()

    const texts = rendered()
    expect(texts.length).toBeGreaterThan(60)
    const unknown = texts.flatMap((text) => nonData(text).filter((token) => !copyTokens.has(token)).map((token) => `${token} ← ${text}`))
    expect(unknown).toEqual([])
    // No sentences: no sentence punctuation, and no run of words longer than a name.
    expect(texts.filter((text) => /[。，！？；]/.test(text))).toEqual([])
    expect(texts.flatMap(nonData).filter((token) => token.length > 8)).toEqual([])
    const described = Array.from(document.querySelectorAll('.sr-only'), (el) => el.textContent?.trim() ?? '').filter(Boolean)
    expect(described.length).toBeGreaterThan(0)
    expect(described.flatMap(nonData).filter((token) => !copyTokens.has(token))).toEqual([])
  })
})
