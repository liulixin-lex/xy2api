<template>
  <div class="space-y-5" data-testid="share-report-details">
    <p v-if="!report" class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.details.noData') }}</p>
    <template v-else>
      <ShareBarList :title="t('admin.shareScheduling.details.cacheClasses')" :values="cacheTokens" :label="labels.cacheClass" :empty="t('admin.shareScheduling.details.noData')" bar-class="bg-emerald-500" />
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.details.sampledNote') }}</p>

      <div v-if="report.scheduler === 'share'" class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700" data-testid="share-report-share-only">
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.details.shareOnly') }}</p>
        <div class="grid gap-6 md:grid-cols-2">
          <ShareBarList :title="t('admin.shareScheduling.details.reasons')" :values="report.details.reasons ?? undefined" :label="labels.reason" :empty="t('admin.shareScheduling.details.noData')" />
          <ShareBarList :title="t('admin.shareScheduling.details.skipped')" :values="skipped" :label="labels.reason" :empty="t('admin.shareScheduling.details.noData')" bar-class="bg-amber-500" />
        </div>
        <div v-if="hot" data-testid="share-hot-migrations">
          <p
            class="rounded-lg px-3 py-2 text-sm"
            :class="hot.over ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200' : 'text-gray-700 dark:text-dark-200'"
            :data-testid="hot.over ? 'share-hot-migrations-over' : 'share-hot-migrations-line'"
          >
            {{ t('admin.shareScheduling.details.hotMigrations', { count: formatNumber(hot.total), ratio: hot.ratio === null ? '—' : formatSharePercent(hot.ratio, 2) }) }}
            <template v-if="hot.over"> · {{ t('admin.shareScheduling.details.hotMigrationsOver', { limit: formatSharePercent(SHARE_HOT_MIGRATION_LIMIT, 1) }) }}</template>
          </p>
          <ShareBarList v-if="hot.total > 0" class="mt-3" :title="t('admin.shareScheduling.details.hotMigrationReasons')" :values="hot.byReason" :label="labels.reason" :empty="t('admin.shareScheduling.details.noData')" bar-class="bg-red-500" />
        </div>
        <p v-if="report.details.waits && report.details.waits.count > 0" class="text-sm text-gray-700 dark:text-dark-200">
          {{ t('admin.shareScheduling.details.waits', { count: formatNumber(report.details.waits.count), hit: formatNumber(report.details.waits.hit), avg: formatShareMs(report.details.waits.avg_ms) }) }}
        </p>
        <p v-if="report.details.attempts" class="text-sm text-gray-700 dark:text-dark-200">
          {{ t('admin.shareScheduling.details.attempts', { count: formatNumber(report.details.attempts), requests: formatNumber(report.details.share_requests ?? 0) }) }}
        </p>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ShareBarList from './ShareBarList.vue'
import type { ShareReport } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { formatNumber } from '@/utils/format'
import { SHARE_HOT_MIGRATION_LIMIT, shareHotMigrationRatio } from '@/utils/shareHealth'
import { formatShareMs, formatSharePercent } from '@/utils/shareScheduling'

const props = defineProps<{ report: ShareReport | null }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()

/** Cache attribution weighed by input tokens. */
const cacheTokens = computed(() => Object.fromEntries(Object.entries(props.report?.details.cache_classes ?? {}).map(([key, value]) => [key, value.tokens])))

/** Warm sessions share scheduling moved on its own, against its selections. */
const hot = computed(() => {
  const migrations = props.report?.details.hot_migrations
  if (!migrations) return null
  const ratio = shareHotMigrationRatio(migrations.total, props.report?.details.share_requests ?? props.report?.current.requests)
  return { total: migrations.total, byReason: migrations.by_reason ?? {}, ratio, over: ratio !== null && ratio > SHARE_HOT_MIGRATION_LIMIT }
})

/** Why accounts were passed over, summed across accounts. */
const skipped = computed(() => {
  const out: Record<string, number> = {}
  for (const account of props.report?.accounts ?? []) {
    for (const [reason, count] of Object.entries(account.skipped ?? {})) out[reason] = (out[reason] ?? 0) + count
  }
  return out
})
</script>
