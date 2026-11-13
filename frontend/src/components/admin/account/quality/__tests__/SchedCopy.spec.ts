import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, h } from 'vue'

import zhSched from '@/i18n/locales/zh/admin/schedQuality'
import enSched from '@/i18n/locales/en/admin/schedQuality'
import { kindName, rejectName } from '@/utils/schedQualityFormat'
import type { SchedAccountDetail, SchedAccountSummary, SchedBoard } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import boardFixture from '@/__tests__/fixtures/schedQuality/board.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import SchedGroupsCell from '../SchedGroupsCell.vue'
import SchedBatchDialog from '../SchedBatchDialog.vue'
import SchedOverridePopover from '../SchedOverridePopover.vue'
import SchedPauseMark from '../SchedPauseMark.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import GroupSchedPolicyField from '@/components/admin/group/GroupSchedPolicyField.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedBoard, openSchedDrawer } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn(), reset: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => true) }) }
})

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
const board = (): SchedBoard => {
  const b = structuredClone(boardFixture) as SchedBoard
  b.accounts.push({ ...structuredClone(b.accounts[0]), account_id: 2, eligible: false, reject: 'model_rate_limited', reject_until: new Date(Date.now() + 60_000).toISOString() })
  return b
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedDrawer()
  closeSchedBoard()
  document.documentElement.dataset.motion = 'reduced'
  api.accountDetail.mockResolvedValue(detail())
  api.groupBoard.mockResolvedValue(board())
  api.getGroupPolicy.mockResolvedValue(policyFixture)
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('quality UI copy', () => {
  it('renders only names, state words and value templates from the copy table', async () => {
    const summary = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
    const Host = defineComponent({
      setup: () => () => [
        h(SchedGroupsCell, { summary, openai: true, groupName: () => '晨星组' }),
        h(SchedGroupsCell, { summary: { ...summary, cooling: [], groups: { total: 2, normal: 0, cooling: 0, nodata: 2 } }, openai: true, groupName: () => '' }),
        h(SchedGroupsCell, {
          summary: { ...summary, overrides: [{ group_id: 0, mode: 'exempt', expires_at: null }, { group_id: 53, mode: 'exempt', expires_at: '2026-11-14T10:30:00Z' }] },
          openai: true,
          groupName: () => '晨星组'
        }),
        h(SchedPauseMark, { globalEnabled: false }),
        h(SchedPauseMark, { globalEnabled: true, stale: true }),
        h(SchedAccountDrawer),
        h(GroupSchedBoard),
        h(GroupSchedPolicyField, { groupId: 53 }),
        h(SchedOverridePopover, { show: true, anchor: null, groups: [{ id: 53, name: '晨星组' }] }),
        h(SchedBatchDialog, { action: 'reset', accounts: [{ id: 1, name: 'acc-1' }, { id: 2, name: 'acc-2' }], anchor: null, groups: [] })
      ]
    })
    mount(Host, { attachTo: document.body, global: { plugins: [i18n] } })
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

  it('names every reject code the board can return (G4 D1) and the auto_states kind (D6) with a short word', () => {
    // Normal path of the board's admission checks, then the codes a second admission check can pass through.
    const codes = [
      'platform_mismatch', 'model_rate_limited', 'not_schedulable', 'quota_auto_pause_5h', 'quota_auto_pause_7d',
      'model_not_supported', 'profit_threshold', 'profit_invalid_account_rate', 'account_scheduling_threshold',
      'privacy_not_set', 'runtime_blocked', 'proxy_stream_quarantined', 'shadow_parent_unhealthy',
      'channel_upstream_restricted', 'channel_pricing_restricted',
      'quota_auto_reset_pending_5h', 'quota_auto_reset_pending_7d', 'quota_auto_reset_credit_check_5h', 'quota_auto_reset_credit_check_7d'
    ]
    const { t, te } = i18n.global
    const zh = codes.map((code) => rejectName(code, t, te))
    expect(zh).not.toContain('其他')
    expect(zh.filter((word) => word.replace(/\s/g, '').length > 4)).toEqual([])
    expect(Object.keys(zhSched.schedQuality.reject).sort()).toEqual([...codes].sort())
    expect(Object.keys(enSched.schedQuality.reject).sort()).toEqual([...codes].sort())
    expect(rejectName('account_nil', t, te)).toBe('其他')
    expect(kindName('auto_states', t, te)).toBe('自动状态')
    expect(zhSched.schedQuality.result.skipped).toBe('未执行')
  })
})
