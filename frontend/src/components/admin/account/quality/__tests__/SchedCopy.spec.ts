import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, h } from 'vue'

import zhSched from '@/i18n/locales/zh/admin/schedQuality'
import enSched from '@/i18n/locales/en/admin/schedQuality'
import { kindName, rejectName } from '@/utils/schedQualityFormat'
import type { BackendOrder } from '@/api/admin/schedQualityTypes'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import SchedBatchDialog from '../SchedBatchDialog.vue'
import SchedOverridePopover from '../SchedOverridePopover.vue'
import SchedPauseMark from '../SchedPauseMark.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import GroupSchedPolicyField from '@/components/admin/group/GroupSchedPolicyField.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedBoard } from '@/composables/useSchedLayers'
import { withSchedHost } from './schedLayerHost'

const api = vi.hoisted(() => ({ groupBoard: vi.fn(), groupsSummary: vi.fn(), getGroupPolicy: vi.fn(), previewGroupPolicy: vi.fn(), reset: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => true) }) }
})

// Acceptance item 3 of WP7: every text the new components render (visible text and tips) is data or comes from
// design/copy.md, and none is a sentence. Existing keys reused by the design (column "操作", close) are listed too.
// The quality panel is not held to it any more: its copy follows the approved prototype of the redesign (spec 5),
// which has empty states and notes. Nor is the board's group summary (ruling H.9): it carries the report's own
// sentences ("users' wait for the first useful output, from entering the gateway …", 5.3 and N11).
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

// Visible text and tips; screen-reader-only descriptions are checked on their own.
function rendered(): string[] {
  const out: string[] = []
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent?.trim()
    if (text && !node.parentElement?.closest('.sr-only, [data-sq-board-summary]')) out.push(text)
  }
  document.querySelectorAll<HTMLElement & { __tipText?: string }>('*').forEach((el) => {
    if (el.__tipText && !el.closest('[data-sq-board-summary]')) out.push(el.__tipText)
  })
  return out
}

const board = (): BackendOrder => {
  const b = structuredClone(orderFixture) as BackendOrder
  b.rows.push({ ...structuredClone(b.rows[0]), account_id: 2, name: 'acc-2', layer: 'stopped', until: new Date(Date.now() + 60_000).toISOString() })
  return b
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedDrawer()
  closeSchedBoard()
  document.documentElement.dataset.motion = 'reduced'
  api.groupBoard.mockResolvedValue(board())
  api.getGroupPolicy.mockResolvedValue(policyFixture)
  api.groupsSummary.mockResolvedValue({ at: '', stale: false, groups: [] })
  api.previewGroupPolicy.mockResolvedValue({
    relaxed_lines: [],
    lines: Object.fromEntries(['ttfo', 'error', 'cache'].map((line) => [line, { now: { pass: 3, unknown: 1, fail: 0 }, after: { pass: 2, unknown: 1, fail: 1 } }])),
    changed: [{ account_id: 7, name: 'acc-07', model: 'gpt-6.1-sol', line: 'error', from: 'pass', to: 'fail' }]
  })
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('quality UI copy', () => {
  it('renders only names, state words and value templates from the copy table', async () => {
    const Host = defineComponent({
      setup: () => () => [
        h(SchedPauseMark, { globalEnabled: false }),
        h(SchedPauseMark, { globalEnabled: true, stale: true }),
        h(GroupSchedBoard),
        h(GroupSchedPolicyField, { groupId: 53 }),
        h(SchedOverridePopover, { show: true, mode: 'exempt', anchor: null, groups: [{ id: 53, name: '晨星组' }] }),
        h(SchedBatchDialog, { action: 'reset', accounts: [{ id: 1, name: 'acc-1' }, { id: 2, name: 'acc-2' }], anchor: null, groups: [] })
      ]
    })
    mount(withSchedHost(Host), { attachTo: document.body, global: { plugins: [i18n] } })
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
    expect(described.flatMap(nonData).filter((token) => !copyTokens.has(token))).toEqual([])
  })

  it('names every reject code the board can return (G4 D1) and the auto_states kind (D6) with a short word', () => {
    // Normal path of the board's admission checks, then the codes a second admission check can pass through.
    const codes = [
      'platform_mismatch', 'model_rate_limited', 'not_schedulable', 'quota_auto_pause_5h', 'quota_auto_pause_7d',
      'model_not_supported', 'profit_threshold', 'profit_floor', 'profit_invalid_account_rate', 'account_scheduling_threshold',
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
