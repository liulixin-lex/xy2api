import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import AccountRecentCell from '../AccountRecentCell.vue'
import { formatShortTime } from '../accountTime'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'
import type { TipContent } from '@/utils/tip'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

type Quality = Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache' | 'reset_at'>
const quality = (patch: Partial<Quality> = {}): Quality => ({
  reset_at: null,
  ttfo: { n: 30, short: false, red: false, p50_ms: 2800 },
  error: { n: 100, short: false, red: false, rate: 0.042 },
  cache: { n: 40, short: false, red: false, ratio: 0.8 },
  ...patch
})
enableAutoUnmount(afterEach)
const mountCell = (q: Quality | null) => mount(AccountRecentCell, { attachTo: document.body, props: { quality: q } })
const cells = (q: Quality | null) =>
  mountCell(q).findAll('.recent__v').map((el) => ({ dim: el.attributes('data-dim'), text: el.text(), cls: el.classes().filter((cls) => cls !== 'recent__v') }))

// Recent status: the whole account's values, never judged in the browser.
describe('AccountRecentCell', () => {
  it('shows the whole account\'s values plainly, even under a red mark that judges one group and model (7k-2)', () => {
    expect(cells(quality({ error: { n: 100, short: false, red: true, rate: 0.12 } }))).toEqual([
      { dim: 'ttfo', text: '2.8s', cls: [] },
      { dim: 'error', text: '12%', cls: [] },
      { dim: 'cache', text: '80%', cls: [] }
    ])
  })

  // One tip card for the whole cell (12.7): the window, then a row per dimension with its value and sample count.
  it('has one card: the last hour, each dimension\'s value and samples, muted with too few samples, "—" without a value', () => {
    const cell = mountCell(quality({ cache: { n: 3, short: true, red: false, ratio: 0.2 }, error: { n: 0, short: true, red: false, rate: null } }))
    expect(cell.findAll('.recent__v').some((value) => '__tipHandlers' in value.element)).toBe(false)
    vi.useFakeTimers()
    cell.element.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    vi.useRealTimers()
    const tip = document.getElementById('sq-tip')!
    expect(tip.querySelector('.sq-tip__title')!.textContent).toBe('admin.accounts.table.tip.recentTitle')
    expect(Array.from(tip.querySelectorAll('.sq-tip__c'), (el) => el.textContent)).toEqual([
      'admin.accounts.table.dims.ttfo', '2.8s', 'admin.accounts.table.tip.count:{"n":30}',
      'admin.accounts.table.dims.error', '—', 'admin.accounts.table.tip.countShort:{"n":0}',
      'admin.accounts.table.dims.cache', '20%', 'admin.accounts.table.tip.countShort:{"n":3}'
    ])
    expect(Array.from(tip.querySelectorAll('.sq-tip__tone--muted'), (el) => el.textContent)).toEqual(['—', '20%'])
    cell.element.dispatchEvent(new Event('mouseleave'))
  })

  it('names the reset time when the stats were reset within the hour; no card without quality data', () => {
    const card = (q: Quality | null) => {
      const value = (mountCell(q).element as HTMLElement & { __tipText?: () => TipContent }).__tipText!()
      return typeof value === 'string' ? value : value.title
    }
    const reset = new Date(Date.now() - 20 * 60_000).toISOString()
    expect(card(quality({ reset_at: reset }))).toBe(`admin.accounts.table.tip.recentSince:${JSON.stringify({ time: formatShortTime(reset) })}`)
    expect(card(quality({ reset_at: new Date(Date.now() - 2 * 3_600_000).toISOString() }))).toBe('admin.accounts.table.tip.recentTitle')
    expect(card(null)).toBe('')
  })

  it('takes its column\'s size, never the viewport\'s: 72px, 84px only when the table has the roomy widths (7q-5)', () => {
    const source = (path: string) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
    // An optional column keeps a 1920px screen in the compact widths: 84px there cut "0.7s" to "0.".
    expect(source('../AccountRecentCell.vue')).not.toContain('@media')
    expect(source('../../../../../views/admin/AccountsView.vue')).toMatch(/\.account-table\.is-roomy :deep\(\.recent:not\(\.recent--card\)\) \{\s*width: 84px;\s*\}/)
  })

  it('reads muted with too few samples, and "—" without data', () => {
    expect(cells(quality({ cache: { n: 3, short: true, red: true, ratio: 0.2 } }))[2]).toEqual({ dim: 'cache', text: '20%', cls: ['recent__v--na'] })
    expect(cells(null).map((cell) => [cell.text, cell.cls])).toEqual([['—', ['recent__v--na']], ['—', ['recent__v--na']], ['—', ['recent__v--na']]])
  })
})
