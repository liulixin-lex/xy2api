import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ShareReportDetails from '../ShareReportDetails.vue'
import { shareReport } from '@/stores/__tests__/shareSchedulingFixtures'
import { SHARE_HOT_MIGRATION_LIMIT, shareHotMigrationRatio } from '@/utils/shareHealth'

const known = new Set(['admin.shareScheduling.reasons.realign_cold', 'admin.shareScheduling.reasons.return_cold'])
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params && Object.keys(params).length ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => known.has(key)
  })
}))

function details(hot: { total: number; by_reason: Record<string, number> } | null, shareRequests: number | null = 5000) {
  return shareReport(6, {
    details: { cache_classes: {}, reasons: {}, waits: null, attempts: null, share_requests: shareRequests, hot_migrations: hot }
  })
}

describe('ShareReportDetails hot migrations', () => {
  it('flags hot migrations above the rollback line and lists their reasons', () => {
    const w = mount(ShareReportDetails, { props: { report: details({ total: 12, by_reason: { realign_cold: 9, legacy_move: 3 } }) } })
    const line = w.get('[data-testid="share-hot-migrations-over"]')
    expect(line.text()).toContain('"count":"12"')
    expect(line.text()).toContain('"ratio":"0.24%"')
    expect(line.text()).toContain('admin.shareScheduling.details.hotMigrationsOver')
    expect(line.classes()).toContain('bg-amber-50')
    const reasons = w.get('[data-testid="share-hot-migrations"]').text()
    expect(reasons).toContain('admin.shareScheduling.reasons.realign_cold')
    // Codes without a label show as they are.
    expect(reasons).toContain('legacy_move')
  })

  it('keeps a low rate plain and hides an empty reason list', () => {
    const w = mount(ShareReportDetails, { props: { report: details({ total: 2, by_reason: { return_cold: 2 } }) } })
    expect(w.find('[data-testid="share-hot-migrations-over"]').exists()).toBe(false)
    expect(w.get('[data-testid="share-hot-migrations-line"]').text()).toContain('"ratio":"0.04%"')

    const none = mount(ShareReportDetails, { props: { report: details({ total: 0, by_reason: {} }) } })
    expect(none.get('[data-testid="share-hot-migrations-line"]').text()).toContain('"count":"0"')
    expect(none.text()).not.toContain('admin.shareScheduling.details.hotMigrationReasons')
  })

  it('leaves the line out when the report has no hot migration figures', () => {
    const w = mount(ShareReportDetails, { props: { report: details(null) } })
    expect(w.find('[data-testid="share-hot-migrations"]').exists()).toBe(false)
  })

  it('needs selections to compute the rate', () => {
    expect(shareHotMigrationRatio(3, 0)).toBeNull()
    expect(shareHotMigrationRatio(3, null)).toBeNull()
    expect(shareHotMigrationRatio(5, 5000)).toBe(SHARE_HOT_MIGRATION_LIMIT)
    // Exactly at the line is not above it.
    const w = mount(ShareReportDetails, { props: { report: details({ total: 5, by_reason: { realign_cold: 5 } }) } })
    expect(w.find('[data-testid="share-hot-migrations-over"]').exists()).toBe(false)
  })
})
