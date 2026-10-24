// M2b: stats split by platform (00-shared/cache_panel_contract/m2b v1.0, README §1.3 and §1.5).
// F's four samples, copied verbatim into ./contract/m2b, pass the production guard and drive the
// two cards that carry OpenAI-only metrics: IdentityCard (pck, session headers) and CoverageCard
// (category completeness). The default platform=openai answer (parent stats_24h) stays as in M2.
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', async () => {
  const zh = (await import('@/i18n/locales/zh')).default as Record<string, unknown>
  const exists = (key: string) => key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], zh) !== undefined
  return {
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
      te: exists,
    }),
  }
})

import CoverageCard from '../components/CoverageCard.vue'
import IdentityCard from '../components/IdentityCard.vue'
import { checkTraceStats } from '../guard'
import type { TraceStatsResponse } from '../types'
import type { CacheChartsState } from '../useCacheCharts'
import { statsScope } from '../viewModel'

interface Sample {
  _contract: string[]
  _note: string
  request: { method: string; path: string; body: unknown }
  status: number
  response: { code: number; data: TraceStatsResponse }
}

const m2b = import.meta.glob('./contract/m2b/*.json', { eager: true, import: 'default' }) as Record<string, Sample>
const parent = import.meta.glob('./contract/stats_24h.json', { eager: true, import: 'default' }) as Record<string, Sample>
const NAMES = Object.keys(m2b).map((p) => p.replace('./contract/m2b/', '').replace('.json', ''))

/** The `data` of a sample after the production guard, as useCacheCharts stores it. */
function stats(name: string): TraceStatsResponse {
  const found = name === 'stats_24h' ? parent['./contract/stats_24h.json'] : m2b[`./contract/m2b/${name}.json`]
  if (!found) throw new Error(`unknown sample ${name}`)
  return checkTraceStats(structuredClone(found.response.data))
}

function mountCards(name: string) {
  const data = stats(name)
  const state = {
    stats: { status: 'ready', data, refreshing: false, error: null },
    filters: { traceWindow: '24h', platform: data.platform ?? 'openai' },
    loadStats: vi.fn(),
  } as unknown as CacheChartsState
  const stubs = { Icon: true }
  return {
    identity: mount(IdentityCard, { props: { state }, global: { stubs } }),
    coverage: mount(CoverageCard, { props: { state }, global: { stubs } }),
  }
}

const tag = (wrapper: ReturnType<typeof mount>, name: 'openaiOnly' | 'bridgeOnly') => wrapper.findAll(`[data-test="scope-tag-${name}"]`)
const metricLabels = (coverage: ReturnType<typeof mount>) => coverage.findAll('dt').map((dt) => dt.text())

describe('M2b stats scope notes on the stats cards (cache_panel_contract/m2b v1.0)', () => {
  it('has the four samples, each accepted by the production guard', () => {
    expect(NAMES.sort()).toEqual(['m2b_stats_all', 'm2b_stats_anthropic', 'm2b_stats_openai_bridge_only', 'm2b_stats_platform_not_covered'])
    for (const name of NAMES) expect(() => stats(name), name).not.toThrow()
  })

  it('marks the OpenAI-only metrics in the all view, where they are the OpenAI partition (m2b_stats_all)', () => {
    expect(statsScope(stats('m2b_stats_all'))).toMatchObject({ openaiOnly: true, openaiMetricsAbsent: false, notCovered: false, bridgeOnly: false })
    const { identity, coverage } = mountCards('m2b_stats_all')
    expect(tag(identity, 'openaiOnly')).toHaveLength(1)
    expect(identity.find('[data-test="pck-out"]').exists()).toBe(true)
    expect(identity.find('[data-test="identity-openai-only"]').exists()).toBe(false)
    const labels = metricLabels(coverage)
    expect(labels).toHaveLength(2)
    expect(labels[1]).toContain('admin.cacheCharts.coverage.categoryKnown')
    expect(labels[1]).toContain('admin.cacheCharts.scopeTags.openaiOnly')
    expect(labels[0]).not.toContain('admin.cacheCharts.scopeTags.openaiOnly')
    expect(coverage.text()).toContain('admin.cacheCharts.coverage.notes.openai_only_metrics')
  })

  it('hides the OpenAI-only metrics in a provider view, where they are [] or null (m2b_stats_anthropic)', () => {
    expect(statsScope(stats('m2b_stats_anthropic'))).toMatchObject({ openaiOnly: true, openaiMetricsAbsent: true })
    const { identity, coverage } = mountCards('m2b_stats_anthropic')
    expect(tag(identity, 'openaiOnly')).toHaveLength(1)
    expect(identity.get('[data-test="identity-openai-only"]').text()).toContain('admin.cacheCharts.identity.openaiOnlyEmpty')
    expect(identity.find('[data-test="pck-out"]').exists()).toBe(false)
    expect(identity.find('[data-test="session-headers"]').exists()).toBe(false)
    const labels = metricLabels(coverage)
    expect(labels).toHaveLength(1)
    expect(labels[0]).toContain('admin.cacheCharts.coverage.traceCoverage')
  })

  it('says when the platform is not observed, once (m2b_stats_platform_not_covered)', () => {
    const { identity, coverage } = mountCards('m2b_stats_platform_not_covered')
    expect(coverage.get('[data-test="coverage-not-covered"]').text()).toContain('admin.cacheCharts.coverage.notes.platform_not_covered')
    expect(coverage.text().split('admin.cacheCharts.coverage.notes.platform_not_covered')).toHaveLength(2)
    expect(coverage.text()).toContain('admin.cacheCharts.coverage.notes.openai_only_metrics')
    expect(identity.find('[data-test="identity-openai-only"]').exists()).toBe(true)
  })

  it('marks the OpenAI view as Messages bridge only, metrics kept (m2b_stats_openai_bridge_only)', () => {
    const { identity, coverage } = mountCards('m2b_stats_openai_bridge_only')
    expect(tag(identity, 'bridgeOnly')).toHaveLength(1)
    expect(tag(coverage, 'bridgeOnly')).toHaveLength(1)
    expect(tag(identity, 'openaiOnly')).toHaveLength(0)
    expect(identity.find('[data-test="pck-out"]').exists()).toBe(true)
    expect(metricLabels(coverage)).toHaveLength(2)
    expect(coverage.text()).toContain('admin.cacheCharts.coverage.notes.openai_native_excluded')
  })

  it('leaves the default platform=openai answer as in M2: no tags, both metrics (stats_24h)', () => {
    const { identity, coverage } = mountCards('stats_24h')
    expect(tag(identity, 'openaiOnly')).toHaveLength(0)
    expect(tag(identity, 'bridgeOnly')).toHaveLength(0)
    expect(tag(coverage, 'bridgeOnly')).toHaveLength(0)
    expect(identity.find('[data-test="pck-out"]').exists()).toBe(true)
    expect(metricLabels(coverage).join(' ')).not.toContain('admin.cacheCharts.scopeTags')
    expect(coverage.find('[data-test="coverage-not-covered"]').exists()).toBe(false)
    expect(coverage.text()).toContain('admin.cacheCharts.coverage.notes.messages_bridge_excluded')
  })
})
