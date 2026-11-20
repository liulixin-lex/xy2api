import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount, type DOMWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import AccountRecentCell from '../AccountRecentCell.vue'
import type { RecentQuality } from '../accountRecent'
import type { TipCard, TipContent } from '@/utils/tip'

// The copy stub, but for the "real messages" test, which installs the app's messages.
const i18nMode = vi.hoisted(() => ({ real: false }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const stub = { t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }
  return { ...actual, useI18n: () => (i18nMode.real ? actual.useI18n() : stub) }
})

type Quality = RecentQuality
// The contract's example: 33 first tokens (2 timed out), 8 of 33 over 10 s, 2 of 50 requests failed, 29 turns.
const quality = (patch: Partial<Quality> = {}): Quality => ({
  recent_since: null,
  first_output: { n: 33, timeouts: 2, p50_ms: 7000 },
  slow_10s: { n: 33, slow: 8, timeouts: 2 },
  error: { n: 50, short: false, red: false, rate: 0.04, failed: 2 },
  cache: { ratio: 0.7, turns: 29 },
  ...patch
})
enableAutoUnmount(afterEach)
afterEach(() => {
  i18nMode.real = false
  vi.useRealTimers()
})
/** Local time frozen at `now` (Date alone): the window's title is local HH:MM. */
const atLocal = (now: Date) => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
}
const mountCell = (q: Quality | null, card = false) => mount(AccountRecentCell, { attachTo: document.body, props: { quality: q, card } })
/** What a value shows, and what a screen reader reads instead when it differs (the table's "T/O"). */
const shown = (el: DOMWrapper<Element>) => {
  const visible = el.find('[aria-hidden="true"]')
  return visible.exists() ? { text: visible.text(), spoken: el.get('.sr-only').text() } : { text: el.text() }
}
const cells = (q: Quality | null, card = false) =>
  mountCell(q, card).findAll('.recent__v').map((el) => ({ dim: el.attributes('data-dim'), ...shown(el), cls: el.classes().filter((cls) => cls !== 'recent__v') }))
const tipOf = (q: Quality | null): TipContent => (mountCell(q).element as HTMLElement & { __tipText?: () => TipContent }).__tipText!()
const rowsOf = (q: Quality) => (tipOf(q) as TipCard).rows!
const json = (key: string, params: Record<string, unknown>) => `admin.accounts.table.tip.${key}:${JSON.stringify(params)}`
const tooFew = (note: string) => json('tooFew', { note })
const muted = (text: string) => ({ text, tone: 'muted' })

// Recent status: the whole account's values, never judged in the browser.
describe('AccountRecentCell', () => {
  it('shows the whole account\'s values plainly, even under a red mark that judges one group and model (7k-2)', () => {
    expect(cells(quality({ error: { n: 100, short: false, red: true, rate: 0.12, failed: 12 } }))).toEqual([
      { dim: 'ttfo', text: '7.0s', cls: [] },
      { dim: 'error', text: '12%', cls: [] },
      { dim: 'cache', text: '70%', cls: [] }
    ])
  })

  it('writes first token as the median in seconds, "超时" when the median is a timeout, "—" without samples; muted under 20', () => {
    const ttfo = (first_output: Quality['first_output'], card = false) => cells(quality({ first_output }), card)[0]
    expect(ttfo({ n: 20, timeouts: 10, p50_ms: 7000 })).toEqual({ dim: 'ttfo', text: '7.0s', cls: [] })
    expect(ttfo({ n: 19, timeouts: 0, p50_ms: 7000 })).toEqual({ dim: 'ttfo', text: '7.0s', cls: ['recent__v--na'] })
    // The table's short word is hidden from screen readers, which read the full one; the card has room for it.
    expect(ttfo({ n: 21, timeouts: 11, p50_ms: null }))
      .toEqual({ dim: 'ttfo', text: 'admin.accounts.table.recent.timeoutShort', spoken: 'admin.accounts.table.recent.timeout', cls: [] })
    expect(ttfo({ n: 21, timeouts: 11, p50_ms: null }, true)).toEqual({ dim: 'ttfo', text: 'admin.accounts.table.recent.timeout', cls: [] })
    expect(ttfo({ n: 0, timeouts: 0, p50_ms: null })).toEqual({ dim: 'ttfo', text: '—', cls: ['recent__v--na'] })
    // A median of 0 ms is a time, not a timeout.
    const instant = quality({ first_output: { n: 30, timeouts: 0, p50_ms: 0 } })
    expect(cells(instant)[0]).toEqual({ dim: 'ttfo', text: '0.00s', cls: [] })
    expect(rowsOf(instant)[0]).toEqual(['admin.accounts.table.recent.firstOutput', '0.00s', json('count', { n: 30 })])
    expect(rowsOf(quality({ first_output: { n: 3, timeouts: 3, p50_ms: null } }))[0])
      .toEqual(['admin.accounts.table.recent.firstOutput', muted('admin.accounts.table.recent.timeout'), tooFew(json('count', { n: 3 }))])
  })

  // One tip card for the whole cell (12.7): the window, then exactly four rows; no first event, gap or all-requests row.
  it('has one card: the window, then the four rows with their samples, muted with too few samples', () => {
    const cell = mountCell(quality({ cache: { ratio: 0.2, turns: 3 } }))
    expect(cell.findAll('.recent__v').some((value) => '__tipHandlers' in value.element)).toBe(false)
    vi.useFakeTimers()
    cell.element.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    vi.useRealTimers()
    const tip = document.getElementById('sq-tip')!
    expect(tip.querySelector('.sq-tip__title')!.textContent).toBe('admin.accounts.table.tip.recentTitle')
    // On a narrow screen the samples wrap; the label and value columns keep their width (utils/tip.ts wrapEnd).
    expect(tip.querySelector('.sq-tip__grid')!.classList.contains('sq-tip__grid--wrap')).toBe(true)
    expect(Array.from(tip.querySelectorAll('.sq-tip__c'), (el) => el.textContent)).toEqual([
      'admin.accounts.table.recent.firstOutput', '7.0s', json('count', { n: 33 }),
      'admin.accounts.table.recent.slow', '24%', json('slowTimeouts', { slow: 8, n: 2 }),
      'admin.accounts.table.dims.error', '4.0%', json('failed', { failed: 2, n: 50 }),
      'admin.accounts.table.recent.cacheTurns', '20%', tooFew(json('turns', { n: 3 }))
    ])
    expect(Array.from(tip.querySelectorAll('.sq-tip__tone--muted'), (el) => el.textContent)).toEqual(['20%'])
    cell.element.dispatchEvent(new Event('mouseleave'))
  })

  it('writes the share over 10 s in whole percent, "<1%" and ">99%" at the ends, the timeouts only when there are some', () => {
    const slow = (slow_10s: Quality['slow_10s']) => rowsOf(quality({ slow_10s }))[1].slice(1)
    expect(slow({ n: 250, slow: 1, timeouts: 0 })).toEqual(['<1%', json('slow', { n: 1 })])
    expect(slow({ n: 250, slow: 249, timeouts: 3 })).toEqual(['>99%', json('slowTimeouts', { slow: 249, n: 3 })])
    expect(slow({ n: 20, slow: 0, timeouts: 0 })).toEqual(['0%', json('slow', { n: 0 })])
    expect(slow({ n: 20, slow: 20, timeouts: 0 })).toEqual(['100%', json('slow', { n: 20 })])
    expect(slow({ n: 19, slow: 5, timeouts: 0 })).toEqual([muted('26%'), tooFew(json('slow', { n: 5 }))])
    expect(slow({ n: 0, slow: 0, timeouts: 0 })).toEqual([muted('—'), tooFew(json('slow', { n: 0 }))])
  })

  it('counts errors as failed of requests and cache in turns, muted with too few (50 requests, 20 turns)', () => {
    expect(rowsOf(quality({ error: { n: 49, short: true, red: false, rate: 1 / 49, failed: 1 } }))[2])
      .toEqual(['admin.accounts.table.dims.error', muted('2.0%'), tooFew(json('failed', { failed: 1, n: 49 }))])
    expect(rowsOf(quality({ error: { n: 0, short: true, red: false, rate: null, failed: 0 } }))[2][1]).toEqual(muted('—'))
    expect(rowsOf(quality({ cache: { ratio: 0.2, turns: 19 } }))[3]).toEqual(['admin.accounts.table.recent.cacheTurns', muted('20%'), tooFew(json('turns', { n: 19 }))])
    expect(rowsOf(quality({ cache: { ratio: 0.2, turns: 20 } }))[3]).toEqual(['admin.accounts.table.recent.cacheTurns', '20%', json('turns', { n: 20 })])
    expect(cells(quality({ cache: { ratio: 0.2, turns: 19 } }))[2]).toEqual({ dim: 'cache', text: '20%', cls: ['recent__v--na'] })
    expect(cells(quality({ cache: { ratio: 0.2, turns: 20 } }))[2]).toEqual({ dim: 'cache', text: '20%', cls: [] })
    expect(cells(null).map((cell) => [cell.text, cell.cls])).toEqual([['—', ['recent__v--na']], ['—', ['recent__v--na']], ['—', ['recent__v--na']]])
  })

  // The backend says when a reset shortened the window; the browser's clock no longer decides. Local HH:MM only, even
  // across midnight: the window is an hour at most.
  it('titles the card with the backend\'s recent_since in local HH:MM, else the last hour; no card without quality data', () => {
    const title = (q: Quality | null) => {
      const value = tipOf(q)
      return typeof value === 'string' ? value : value.title
    }
    atLocal(new Date(2026, 10, 20, 0, 5))
    expect(title(quality({ recent_since: new Date(2026, 10, 19, 23, 55).toISOString() }))).toBe(json('recentSince', { time: '23:55' }))
    expect(title(quality({ recent_since: new Date(2026, 10, 19, 21, 7).toISOString() }))).toBe(json('recentSince', { time: '21:07' }))
    const resetJustNow = { ...quality(), reset_at: new Date(2026, 10, 20, 0, 4).toISOString() } as Quality
    expect(title(resetJustNow)).toBe('admin.accounts.table.tip.recentTitle')
    expect(title(null)).toBe('')
  })

  // The tip opens from the keyboard too: the cell takes focus when it has a tip, and the tip describes it (v-tip).
  it('takes keyboard focus when it has a tip, shows the card and is described by it; nothing to focus without quality data', () => {
    const cell = mountCell(quality())
    expect(cell.attributes('tabindex')).toBe('0')
    expect(cell.classes()).toContain('sq-focus')
    ;(cell.element as HTMLElement).focus()
    const tip = document.getElementById('sq-tip')!
    expect(tip.hidden).toBe(false)
    expect(tip.querySelectorAll('.sq-tip__c')).toHaveLength(12)
    expect(cell.attributes('aria-describedby')).toBe('sq-tip')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(tip.hidden).toBe(true)
    expect(cell.attributes('aria-describedby')).toBeUndefined()
    expect(mountCell(null).attributes('tabindex')).toBeUndefined()
  })

  it('reads as the contract writes it with the real messages, in Chinese and English', () => {
    i18nMode.real = true
    const read = (locale: 'zh' | 'en', q: Quality, card = false) => {
      const i18n = createI18n({ legacy: false, locale, messages: { en, zh } as never })
      const wrapper = mount(AccountRecentCell, { attachTo: document.body, props: { quality: q, card }, global: { plugins: [i18n] } })
      const tip = (wrapper.element as HTMLElement & { __tipText?: () => TipCard }).__tipText!()
      return { cell: wrapper.findAll('.recent__k, .recent__v').map((el) => shown(el).text), title: tip.title, rows: tip.rows }
    }
    expect(read('zh', quality())).toEqual({
      cell: ['首字', '7.0s', '报错', '4.0%', '缓存', '70%'],
      title: '近 1 小时',
      rows: [['首字中位', '7.0s', '33 次'], ['超过 10 秒', '24%', '8 次，含超时 2 次'], ['报错', '4.0%', '2/50 次请求'], ['缓存·续接轮', '70%', '29 轮']]
    })
    const thin = quality({
      first_output: { n: 12, timeouts: 7, p50_ms: null },
      slow_10s: { n: 1, slow: 1, timeouts: 0 },
      error: { n: 1, short: true, red: false, rate: 0, failed: 0 },
      cache: { ratio: null, turns: 0 },
      recent_since: new Date(2026, 10, 19, 23, 55).toISOString()
    })
    atLocal(new Date(2026, 10, 20, 0, 5))
    expect(read('zh', thin)).toEqual({
      cell: ['首字', '超时', '报错', '0%', '缓存', '—'],
      title: '23:55 起',
      rows: [
        ['首字中位', muted('超时'), '12 次 · 样本不足'],
        ['超过 10 秒', muted('100%'), '1 次 · 样本不足'],
        ['报错', muted('0%'), '0/1 次请求 · 样本不足'],
        ['缓存·续接轮', muted('—'), '0 轮 · 样本不足']
      ]
    })
    expect(read('en', quality()).rows).toEqual([
      ['TTFT median', '7.0s', '33 samples'], ['Over 10s', '24%', '8 slow (2 timeouts)'], ['Errors', '4.0%', '2/50 requests'], ['Cache · turns', '70%', '29 turns']
    ])
    expect(read('en', thin).title).toBe('Since 23:55')
    expect(read('en', quality({ slow_10s: { n: 33, slow: 8, timeouts: 1 } })).rows[1][2]).toBe('8 slow (1 timeout)')
    expect(read('en', thin).rows.map((row) => row[2])).toEqual(['12 samples · too few', '1 slow · too few', '0/1 request · too few', '0 turns · too few'])
    expect(read('en', thin).cell.slice(0, 2)).toEqual(['First', 'T/O'])
    expect(mount(AccountRecentCell, { props: { quality: thin }, global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en, zh } as never })] } })
      .get('[data-dim="ttfo"] .sr-only').text()).toBe('Timeout')
    expect(read('en', thin, true).cell.slice(0, 2)).toEqual(['TTFT', 'Timeout'])
  })

  // N1: the cell is 72px (label 24 + gap 8 + value ≤ 40): "100ms" (~42px) does not fit, so first token is written in
  // seconds, hundredths under a second, and the tip card and the panel write it the same way. The widest values the
  // cell can show stay "0.99s", "59.9s", "100%".
  it('writes first token in seconds, the same in the 72px cell and its card', () => {
    // A variable, not a literal: Vite rewrites new URL('<literal>', import.meta.url) into an asset URL.
    const file = '../AccountRecentCell.vue'
    const source = readFileSync(fileURLToPath(new URL(file, import.meta.url)), 'utf8')
    expect(source).toMatch(/\.recent \{[^}]*width: 72px;[^}]*\}/)
    // Label ≤ 25 + gap 6 + value ≤ 36.3 (system fonts) in 72px; a wider label gives way with an ellipsis, never the value.
    expect(source).toMatch(/\.recent \{[^}]*grid-template-columns: minmax\(0, auto\) auto;[^}]*column-gap: 6px;/)
    expect(source).toMatch(/\.recent__k \{[^}]*overflow: hidden;[^}]*text-overflow: ellipsis;[^}]*white-space: nowrap;/)
    const fast = quality({ first_output: { n: 30, timeouts: 0, p50_ms: 100 } })
    expect(cells(fast)[0].text).toBe('0.10s')
    expect(rowsOf(fast)[0][1]).toBe('0.10s')
    const ttfo = (ms: number) => cells(quality({ first_output: { n: 30, timeouts: 0, p50_ms: ms } }))[0].text
    expect([50, 994, 996, 1000, 59_940, 99_960, 123_400].map(ttfo)).toEqual(['0.05s', '0.99s', '1.0s', '1.0s', '59.9s', '100s', '123s'])
  })

  // English "Errors"/"Cache" (34/36px) leave no room in 72px: the table cell has short labels, the card and tip the full ones.
  it('labels the table cell with the short names and the card and its tip with the full ones', () => {
    const labels = (card: boolean) => mountCell(quality(), card).findAll('.recent__k').map((el) => el.text())
    expect(labels(false)).toEqual(['admin.accounts.table.dimsShort.ttfo', 'admin.accounts.table.dimsShort.error', 'admin.accounts.table.dimsShort.cache'])
    expect(labels(true)).toEqual(['admin.accounts.table.dims.ttfo', 'admin.accounts.table.dims.error', 'admin.accounts.table.dims.cache'])
  })

  it('takes its column\'s size, never the viewport\'s: 72px, 84px only when the table has the roomy widths (7q-5)', () => {
    const source = (path: string) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
    // An optional column keeps a 1920px screen in the compact widths: 84px there cut "0.7s" to "0.".
    expect(source('../AccountRecentCell.vue')).not.toContain('@media')
    expect(source('../../../../../views/admin/AccountsView.vue')).toMatch(/\.account-table\.is-roomy :deep\(\.recent:not\(\.recent--card\)\) \{\s*width: 84px;\s*\}/)
  })
})
