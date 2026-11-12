import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, h } from 'vue'

import zhSched from '@/i18n/locales/zh/admin/schedQuality'
import type { SchedAccountDetail, SchedAccountSummary, SchedBoard } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import boardFixture from '@/__tests__/fixtures/schedQuality/board.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import resetFixture from '@/__tests__/fixtures/schedQuality/reset.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import SchedGroupsCell from '../SchedGroupsCell.vue'
import SchedNumberCell from '../SchedNumberCell.vue'
import SchedResetDialog from '../SchedResetDialog.vue'
import SchedBatchDialog from '../SchedBatchDialog.vue'
import SchedOverridePopover from '../SchedOverridePopover.vue'
import SchedPauseMark from '../SchedPauseMark.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import GroupSchedPolicyField from '@/components/admin/group/GroupSchedPolicyField.vue'
import { closeSchedBoard, closeSchedDrawer, closeSchedReset, openSchedBoard, openSchedDrawer, openSchedReset } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn(), reset: vi.fn(), startTest: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))

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
const DATA = [/gpt-[\w.-]+/g, /acc-[\w-]+|…|晨星组|北辰组/g, /[\d.:,%/+#×s·—-]+/g]
function nonData(text: string) {
  let rest = text
  for (const pattern of DATA) rest = rest.replace(pattern, ' ')
  return rest.split(/\s+/).filter(Boolean)
}

function rendered(): string[] {
  const out: string[] = []
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent?.trim()
    if (text) out.push(text)
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
const board = (): SchedBoard => {
  const b = structuredClone(boardFixture) as SchedBoard
  b.accounts.push({ ...structuredClone(b.accounts[0]), account_id: 2, eligible: false, reject: 'rate_limit', reject_until: new Date(Date.now() + 60_000).toISOString() })
  return b
}

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedDrawer()
  closeSchedBoard()
  closeSchedReset()
  document.documentElement.dataset.motion = 'reduced'
  api.accountDetail.mockResolvedValue(detail())
  api.groupBoard.mockResolvedValue(board())
  api.getGroupPolicy.mockResolvedValue(policyFixture)
  api.reset.mockResolvedValue(resetFixture)
  api.startTest.mockResolvedValue({ tests: [{ model: 'gpt-6.1-sol', test_id: 'x', started: true, why: '' }] })
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('quality UI copy', () => {
  it('renders only names, state words and value templates from the copy table', async () => {
    const summary = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
    const Host = defineComponent({
      setup: () => () => [
        h(SchedNumberCell, { accountId: 479, dim: 'ttfo', summary }),
        h(SchedGroupsCell, { summary, groupName: () => '晨星组' }),
        h(SchedGroupsCell, { summary: { ...summary, cooling: [], groups: { total: 2, normal: 0, cooling: 0, nodata: 2 } }, groupName: () => '' }),
        h(SchedGroupsCell, { summary: { ...summary, cooling: [{ ...summary.cooling[0], testing: true }], cooling_more: 2 }, groupName: () => '北辰组' }),
        h(SchedPauseMark, { globalEnabled: false }),
        h(SchedPauseMark, { globalEnabled: true, stale: true }),
        h(SchedAccountDrawer),
        h(GroupSchedBoard),
        h(SchedResetDialog),
        h(GroupSchedPolicyField, { groupId: 53 }),
        h(SchedOverridePopover, { show: true, mode: 'demote', anchor: null, groups: [{ id: 53, name: '晨星组' }] }),
        h(SchedBatchDialog, { action: 'test', accounts: [{ id: 1, name: 'acc-1' }, { id: 2, name: 'acc-2' }], anchor: null, groups: [] })
      ]
    })
    mount(Host, { attachTo: document.body, global: { plugins: [i18n] } })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    openSchedBoard({ id: 53, name: '晨星组' })
    openSchedReset({ id: 479, name: 'acc-03' })
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-reset-confirm]')!.click()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.click()
    await flushPromises()

    const texts = rendered()
    expect(texts.length).toBeGreaterThan(60)
    const unknown = texts.flatMap((text) => nonData(text).filter((token) => !copyTokens.has(token)).map((token) => `${token} ← ${text}`))
    expect(unknown).toEqual([])
    // No sentences: no sentence punctuation, and no run of words longer than a name.
    expect(texts.filter((text) => /[。，！？；]/.test(text))).toEqual([])
    expect(texts.flatMap(nonData).filter((token) => token.length > 8)).toEqual([])
  })
})
