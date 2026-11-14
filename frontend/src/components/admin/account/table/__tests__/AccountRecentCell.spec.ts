import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import AccountRecentCell from '../AccountRecentCell.vue'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

type Quality = Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache'>
const quality = (patch: Partial<Quality> = {}): Quality => ({
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
    const value = mountCell(quality()).get('[data-dim="ttfo"]').element
    vi.useFakeTimers()
    value.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    vi.useRealTimers()
    expect(document.getElementById('sq-tip')!.textContent).toBe('admin.accounts.table.recentValue:{"label":"admin.accounts.table.dims.ttfo","value":"2.8s"}\nadmin.accounts.table.recentScope')
    value.dispatchEvent(new Event('mouseleave'))
  })

  it('reads muted with too few samples, and "—" without data', () => {
    expect(cells(quality({ cache: { n: 3, short: true, red: true, ratio: 0.2 } }))[2]).toEqual({ dim: 'cache', text: '20%', cls: ['recent__v--na'] })
    expect(cells(null).map((cell) => [cell.text, cell.cls])).toEqual([['—', ['recent__v--na']], ['—', ['recent__v--na']], ['—', ['recent__v--na']]])
  })
})
