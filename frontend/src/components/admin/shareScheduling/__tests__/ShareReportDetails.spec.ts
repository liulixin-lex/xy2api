import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ShareReportDetails from '../ShareReportDetails.vue'
import { shareReport } from '@/stores/__tests__/shareSchedulingFixtures'
import { SHARE_HOT_MIGRATION_LIMIT, shareHotMigrationRatio } from '@/utils/shareHealth'

const known = new Set([
  'admin.shareScheduling.reasons.rebind_unavailable',
  'admin.shareScheduling.reasons.failover_next',
  'admin.shareScheduling.reasons.resil_failure_streak',
  'admin.shareScheduling.reasons.spill_rebind'
])
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params && Object.keys(params).length ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => known.has(key)
  })
}))

type Hot = { total: number; active: number; by_reason: Record<string, number> }

function details(hot: Hot | null, shareRequests: number | null = 5000, waits: { count: number; hit: number; avg_ms: number | null } | null = null) {
  return shareReport(6, {
    details: { cache_classes: {}, reasons: {}, waits, attempts: null, share_requests: shareRequests, hot_migrations: hot }
  })
}

describe('ShareReportDetails hot migrations', () => {
  // The readiness review's shadow period (B2): every warm move came from an
  // outage or a failure, none from share scheduling itself.
  const passive: Hot = { total: 488, active: 0, by_reason: { rebind_unavailable: 306, failover_next: 150, resil_failure_streak: 32 } }

  it('does not call passive warm moves a reason to go back', () => {
    const w = mount(ShareReportDetails, { props: { report: details(passive, 8372) } })
    expect(w.find('[data-testid="share-hot-migrations-over"]').exists()).toBe(false)
    expect(w.find('[data-testid="share-hot-migrations-active"]').exists()).toBe(false)
    const total = w.get('[data-testid="share-hot-migrations-total"]')
    expect(total.text()).toContain('admin.shareScheduling.details.hotMigrationsTotal')
    expect(total.text()).toContain('"count":"488"')
    expect(total.classes()).not.toContain('bg-amber-50')
    const reasons = w.get('[data-testid="share-hot-migrations"]').text()
    expect(reasons).toContain('admin.shareScheduling.reasons.rebind_unavailable')
    expect(reasons).toContain('admin.shareScheduling.reasons.failover_next')
  })

  it('flags warm moves share scheduling made itself above the rollback line', () => {
    const w = mount(ShareReportDetails, { props: { report: details({ total: 20, active: 12, by_reason: { spill_rebind: 12, legacy_move: 8 } }) } })
    const line = w.get('[data-testid="share-hot-migrations-over"]')
    expect(line.text()).toContain('admin.shareScheduling.details.hotMigrationsActive')
    expect(line.text()).toContain('"count":"12"')
    expect(line.text()).toContain('"ratio":"0.24%"')
    expect(line.text()).toContain('admin.shareScheduling.details.hotMigrationsOver')
    expect(line.classes()).toContain('bg-amber-50')
    expect(w.get('[data-testid="share-hot-migrations-total"]').text()).toContain('"count":"20"')
    // Codes without a label show as they are.
    expect(w.get('[data-testid="share-hot-migrations"]').text()).toContain('legacy_move')
  })

  it('shows active moves below the line without the warning', () => {
    const w = mount(ShareReportDetails, { props: { report: details({ total: 9, active: 2, by_reason: { spill_rebind: 9 } }) } })
    expect(w.find('[data-testid="share-hot-migrations-over"]').exists()).toBe(false)
    expect(w.get('[data-testid="share-hot-migrations-active"]').text()).toContain('"ratio":"0.04%"')
  })

  it('hides the reason list when nothing moved and the line without figures', () => {
    const none = mount(ShareReportDetails, { props: { report: details({ total: 0, active: 0, by_reason: {} }) } })
    expect(none.get('[data-testid="share-hot-migrations-total"]').text()).toContain('"count":"0"')
    expect(none.text()).not.toContain('admin.shareScheduling.details.hotMigrationReasons')
    const missing = mount(ShareReportDetails, { props: { report: details(null) } })
    expect(missing.find('[data-testid="share-hot-migrations"]').exists()).toBe(false)
  })

  it('needs selections to compute the rate, and exactly at the line is not above it', () => {
    expect(shareHotMigrationRatio(3, 0)).toBeNull()
    expect(shareHotMigrationRatio(3, null)).toBeNull()
    expect(shareHotMigrationRatio(5, 5000)).toBe(SHARE_HOT_MIGRATION_LIMIT)
    const w = mount(ShareReportDetails, { props: { report: details({ total: 5, active: 5, by_reason: { spill_rebind: 5 } }) } })
    expect(w.find('[data-testid="share-hot-migrations-over"]').exists()).toBe(false)
  })

  it('shows waits without an average as a dash', () => {
    const w = mount(ShareReportDetails, { props: { report: details(null, 5000, { count: 3, hit: 1, avg_ms: null }) } })
    expect(w.text()).toContain('"avg":"-"')
  })
})
