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
import { formatShareMs } from '@/utils/shareScheduling'

const props = defineProps<{ report: ShareReport | null }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()

/** Cache attribution weighed by input tokens. */
const cacheTokens = computed(() => Object.fromEntries(Object.entries(props.report?.details.cache_classes ?? {}).map(([key, value]) => [key, value.tokens])))

/** Why accounts were passed over, summed across accounts. */
const skipped = computed(() => {
  const out: Record<string, number> = {}
  for (const account of props.report?.accounts ?? []) {
    for (const [reason, count] of Object.entries(account.skipped ?? {})) out[reason] = (out[reason] ?? 0) + count
  }
  return out
})
</script>
