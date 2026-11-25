/**
 * The decision titles with the real copy, in both languages (plan item 18, Astra review of fe-min, suggestion 1):
 * whether a record is of the previous rules is decided by its rule version alone. A record with no version (from
 * before lines and evidence were kept) is; a 4.0 reset or relax without a line is not, and keeps its muted look.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import type { BackendDecision } from '@/api/admin/schedQualityTypes'
import { basis } from '@/__tests__/fixtures/schedQuality/lines'
import SchedDecisions from '../SchedDecisions.vue'

const api = vi.hoisted(() => ({ accountDecisions: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))

enableAutoUnmount(afterEach)
afterEach(() => {
  document.body.innerHTML = ''
})

const at = (m: number) => new Date(Date.now() + m * 60_000).toISOString()
const record = (id: number, patch: Partial<BackendDecision>): BackendDecision =>
  ({ id, at: at(-id), group_id: 53, model: 'gpt-b', line: '', action: 'reset', level: 0, until: null, basis: null, ...patch })

describe.each([
  ['zh', { legacy: '旧规则 · {action}', renew: '续期', reset: '人工恢复', relax: '放宽重判', enter: '进后排', deadline: '到期回正常' }],
  ['en', { legacy: 'Previous rules · {action}', renew: 'Renewed', reset: 'Recovered by hand', relax: 'Relaxed, judged again', enter: 'To the rear', deadline: 'Back to normal at the deadline' }]
] as const)('decision titles (%s)', (locale, words) => {
  it('marks the previous rules by the rule version alone: no version is old, a 4.0 record without a line is not', async () => {
    api.accountDecisions.mockResolvedValueOnce([
      record(1, { action: 'reset', basis: basis('ttfo') }),
      record(2, { action: 'relax', basis: basis('cache', { kind: 'relax' }) }),
      record(3, { action: 'renew' }),
      record(4, { action: 'enter', line: 'ttfo', until: at(60), basis: basis('ttfo', { rule_version: '3.1' }) }),
      record(5, { action: 'trial_timeout', line: 'ttfo', basis: basis('ttfo', { kind: 'trial', release_reason: 'deadline' }) })
    ])
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } as never })
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, group: () => ({ name: 'G' }), model: 'gpt-b' }, global: { plugins: [i18n] } })
    await flushPromises()
    const what = (id: number) => w.get(`[data-qp-decision="${id}"] .drow__what`)
    const legacy = (action: string) => words.legacy.replace('{action}', action)
    expect([1, 2, 3, 4, 5].map((id) => what(id).text())).toEqual([
      words.reset, words.relax, legacy(words.renew), legacy(words.enter), words.deadline
    ])
    // Without a line a record keeps its muted look, of whichever version.
    expect([1, 2, 3].map((id) => what(id).classes())).toEqual([1, 2, 3].map(() => expect.arrayContaining(['drow__what--muted'])))
  })
})
