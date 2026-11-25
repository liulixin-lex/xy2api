/**
 * An answer from a backend before round 5 (a blue-green overlap, a cached answer) has no until_line, level or
 * trial_started_at, and a model's end may be empty: the group tips show what is known with the real translations,
 * never a key, NaN or a guessed line, and "+N" still finds the model that really ends last (Astra review of fe, 1–3).
 */
import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

import zh from '@/i18n/locales/zh'
import AccountGroupPills from '../AccountGroupPills.vue'
import { formatShortTime } from '../accountTime'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'
import type { AdminGroup } from '@/types'
import type { TipCard, TipContent } from '@/utils/tip'

enableAutoUnmount(afterEach)
afterEach(() => {
  document.body.innerHTML = ''
})

type Quality = Pick<BackendAccountSummary, 'group_status' | 'stop' | 'rear' | 'trial' | 'exemptions'>
const at = (minute: number) => new Date(Date.UTC(2026, 10, 19, 10, minute)).toISOString()
const reasons = zh.admin.accounts.table.dimReasons
const NBSP = ' '

/** The card of group 1's dot, or of "+N" with ten groups, as the tip shows it. */
function card(patch: Partial<Quality>, folded = false): TipCard {
  const wrapper = mount(AccountGroupPills, {
    attachTo: document.body,
    props: {
      account: { id: 7, name: 'synthetic' } as never,
      groups: Array.from({ length: folded ? 10 : 1 }, (_, i) => ({ id: i + 1, name: `G${i + 1}`, platform: 'openai', status: 'active' }) as AdminGroup),
      quality: { group_status: {}, stop: null, rear: [], trial: [], exemptions: [], ...patch }
    },
    global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh } })] }
  })
  const el = wrapper.get(folded ? '[data-test="group-pill-more-text"]' : '[data-test="group-pill-dot"]').element as HTMLElement & { __tipText?: TipContent | (() => TipContent) }
  return (typeof el.__tipText === 'function' ? el.__tipText() : el.__tipText) as TipCard
}
const noKeys = (value: TipCard) => expect(JSON.stringify(value)).not.toMatch(/admin\.[a-zA-Z.]+|NaN|undefined|Invalid Date/)

describe('AccountGroupPills with an older answer', () => {
  it.each([[undefined], [null]])('lists the lines at the back together beside the review, without a step, when until_line and level are %s', (missing) => {
    const rear = [{ group_id: 1, model: 'm', lines: ['ttfo', 'error'], until_line: missing, until: at(40), level: missing, exempt: false, effective: true }] as Quality['rear']
    const value = card({ group_status: { 1: 'rear' }, rear })
    noKeys(value)
    expect(value.rows).toEqual([['m', { text: `${reasons.ttfo}、${reasons.error}`, tone: 'back' }, `${formatShortTime(at(40))}${NBSP}复核`]])
  })

  it('marks the line of an old trial unknown and keeps its end', () => {
    const value = card({ group_status: { 1: 'trial' }, trial: [{ group_id: 1, model: 'm', until: at(120) }] as Quality['trial'] })
    noKeys(value)
    expect(value.rows).toEqual([['m', { text: '—', tone: 'muted' }, `—${NBSP}起 · 最晚${NBSP}${formatShortTime(at(120))}`]])
  })

  it('names no line under "+N" when the answer does not say which', () => {
    const rear = [{ group_id: 9, model: 'm', lines: ['cache'], until: at(40), exempt: false, effective: true }] as Quality['rear']
    const value = card({ group_status: { 9: 'rear' }, rear }, true)
    noKeys(value)
    expect(value.rows![0][2]).toBe(`${formatShortTime(at(40))}${NBSP}复核`)
  })

  it.each(['', null, undefined])('finds the model that ends last under "+N" when another has until=%s, before or after it', (until) => {
    const unknownEnd = { group_id: 9, model: 'a', lines: ['ttfo'], until_line: 'ttfo', until, level: 0, exempt: false, effective: true }
    const known = { group_id: 9, model: 'b', lines: ['error'], until_line: 'error', until: at(180), level: 4, exempt: false, effective: true }
    const shown = `${zh.admin.schedQuality.dim.error} · ${formatShortTime(at(180))}${NBSP}复核`
    for (const rear of [[unknownEnd, known], [known, unknownEnd]] as Quality['rear'][]) {
      const value = card({ group_status: { 9: 'rear' }, rear }, true)
      expect(value.rows![0]).toEqual(['G9', expect.anything(), shown])
    }
  })
})
