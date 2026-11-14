import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountRecentCell from '../AccountRecentCell.vue'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

type Quality = Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache' | 'stop'>
const quality = (patch: Partial<Quality> = {}): Quality => ({
  stop: null,
  ttfo: { n: 30, short: false, red: false, p50_ms: 2800 },
  error: { n: 100, short: false, red: false, rate: 0.042 },
  cache: { n: 40, short: false, red: false, ratio: 0.8 },
  ...patch
})
const cells = (q: Quality | null) =>
  mount(AccountRecentCell, { props: { quality: q } }).findAll('.recent__v').map((el) => ({ dim: el.attributes('data-dim'), text: el.text(), cls: el.classes().filter((cls) => cls !== 'recent__v') }))

// Recent status (7d-12): the backend's marks decide, the cell compares nothing.
describe('AccountRecentCell', () => {
  it('marks a value over the line amber, and only from the backend\'s red mark', () => {
    expect(cells(quality({ error: { n: 100, short: false, red: true, rate: 0.12 } }))).toEqual([
      { dim: 'ttfo', text: '2.8s', cls: [] },
      { dim: 'error', text: '12%', cls: ['recent__v--over'] },
      { dim: 'cache', text: '80%', cls: [] }
    ])
    // A stopped account is not marked over the line.
    expect(cells(quality({ stop: { reason: 'manual' } as never, error: { n: 100, short: false, red: true, rate: 0.12 } }))[1].cls).toEqual([])
  })

  it('reads muted with too few samples, and "—" without data', () => {
    expect(cells(quality({ cache: { n: 3, short: true, red: true, ratio: 0.2 } }))[2]).toEqual({ dim: 'cache', text: '20%', cls: ['recent__v--na'] })
    expect(cells(null).map((cell) => [cell.text, cell.cls])).toEqual([['—', ['recent__v--na']], ['—', ['recent__v--na']], ['—', ['recent__v--na']]])
  })
})
