import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountRecentStatsCell from '../AccountRecentStatsCell.vue'
import type { AccountRecentStats } from '@/types/schedQuality'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      ({
        'admin.schedQuality.dim.ttfo': '首字',
        'admin.schedQuality.dim.error': '错误',
        'admin.schedQuality.dim.cache': '缓存',
        'admin.schedQuality.list.recent': '近期状况',
        'admin.schedQuality.recent.sep': '，',
        'admin.schedQuality.recent.samples': `${params?.n} 次`
      })[key] ?? key
  })
}))

type TipTarget = HTMLElement & { __tipText?: string }

const RouterLinkStub = defineComponent({
  name: 'RouterLink',
  props: ['to'],
  setup(props, { slots, attrs }) {
    return () => h('a', { ...attrs, href: `${props.to.path}?account_id=${props.to.query.account_id}` }, slots.default?.())
  }
})

const stats = (patch: Partial<AccountRecentStats> = {}): AccountRecentStats => ({
  account_id: 7,
  ttft: { p50_ms: 2800, n: 37, tier: 'good' },
  error: { rate: 0.02, bad: 2, n: 100, tier: 'mid' },
  cache: { ratio: 0.62, n: 52, tier: 'bad' },
  ...patch
})
const cell = (props: Record<string, unknown>) =>
  mount(AccountRecentStatsCell, { props: { accountId: 7, ...props }, global: { stubs: { RouterLink: RouterLinkStub } } })
const values = (w: ReturnType<typeof cell>) => w.findAll('.acc-recent__v').map((v) => v.text())
const labels = (w: ReturnType<typeof cell>) => w.findAll('.acc-recent__l').map((v) => v.text())

describe('AccountRecentStatsCell', () => {
  it('shows first token, errors and cache in three fixed lines, coloured only by the backend tier', () => {
    const w = cell({ stats: stats() })
    expect(labels(w)).toEqual(['首字', '错误', '缓存'])
    expect(values(w)).toEqual(['2.8s', '2.0%', '62%'])
    const tiers = w.findAll('.acc-recent__v').map((v) => v.classes().filter((c) => c.startsWith('acc-recent__v--')))
    expect(tiers).toEqual([['acc-recent__v--good'], ['acc-recent__v--mid'], ['acc-recent__v--bad']])
    expect(w.get('[data-test="recent-stats"]').attributes('aria-label')).toBe('首字 2.8s，错误 2.0%，缓存 62%')
  })

  it('formats seconds and percentages and leaves a value under the sample minimum uncoloured', () => {
    const w = cell({
      stats: stats({
        ttft: { p50_ms: 123_400, n: 3, tier: null },
        error: { rate: 0.12, bad: 12, n: 100, tier: 'bad' },
        cache: { ratio: 0.925, n: 6, tier: 'good' }
      })
    })
    expect(values(w)).toEqual(['123s', '12%', '93%'])
    expect(w.get('[data-test="recent-ttft"]').classes()).toEqual(['acc-recent__v'])
    expect(cell({ stats: stats({ error: { rate: 0, bad: 0, n: 100, tier: 'good' } }) }).get('[data-test="recent-error"]').text()).toBe('0%')
  })

  it('puts the sample count in each line\'s tip, and none for errors while ops monitoring is off', () => {
    const w = cell({ stats: stats({ error: null }), link: true })
    expect((w.get('[data-test="recent-ttft"]').element as TipTarget).__tipText).toBe('37 次')
    expect((w.get('[data-test="recent-error"]').element as TipTarget).__tipText).toBe('')
    expect(w.get('[data-test="recent-error"]').text()).toBe('—')
    expect(w.get('[data-test="recent-error"]').classes()).toContain('acc-recent__v--none')
    // Keyboard focus on the cell shows the counts it has.
    expect((w.get('a').element as TipTarget).__tipText).toBe('首字 37 次\n缓存 52 次')
  })

  // Scenario 33: one cell, three lines; values, gaps, nothing and loading all use the same fixed-height grid.
  it('keeps one cell shape for values, a gap, nothing and loading; nothing is a single dash without labels', () => {
    const full = cell({ stats: stats() })
    const gap = cell({ stats: stats({ ttft: { p50_ms: null, n: 0, tier: null } }) })
    const none = cell({ stats: stats({ ttft: { p50_ms: null, n: 0, tier: null }, error: { rate: null, bad: 0, n: 0, tier: null }, cache: { ratio: null, n: 0, tier: null } }) })
    const missing = cell({})
    const loading = cell({ loading: true })
    for (const w of [full, gap, none, missing, loading]) expect(w.get('[data-test="recent-stats"]').classes()).toContain('acc-recent')

    expect(values(gap)).toEqual(['—', '2.0%', '62%'])
    expect(labels(gap)).toEqual(['首字', '错误', '缓存'])
    for (const w of [none, missing]) {
      expect(w.get('[data-test="recent-stats"]').text()).toBe('—')
      expect(w.findAll('[data-test="recent-empty"]')).toHaveLength(1)
      expect(labels(w)).toEqual([])
      expect(w.get('[data-test="recent-stats"]').attributes('aria-label')).toBe('近期状况 —')
    }
    expect(loading.findAll('[data-test="recent-skeleton"]')).toHaveLength(3)
    expect(labels(loading)).toEqual([])
    // Data already shown stays while a refresh is loading.
    expect(values(cell({ stats: stats(), loading: true }))).toEqual(['2.8s', '2.0%', '62%'])
  })

  it('links the whole cell to the account\'s usage records only with that permission', () => {
    const linked = cell({ stats: stats(), link: true })
    expect(linked.get('[data-test="recent-stats"]').element.tagName).toBe('A')
    expect(linked.get('a').attributes('href')).toBe('/admin/usage?account_id=7')
    const plain = cell({ stats: stats() })
    expect(plain.get('[data-test="recent-stats"]').element.tagName).toBe('DIV')
    expect(plain.find('a').exists()).toBe(false)
    expect(plain.get('[data-test="recent-stats"]').attributes('tabindex')).toBeUndefined()
  })
})
