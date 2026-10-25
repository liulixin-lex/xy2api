<template>
  <SectionShell
    id="breakdown"
    :title="t('admin.cacheCharts.breakdown.title')"
    :subtitle="t('admin.cacheCharts.breakdown.subtitle', { window: state.filters.summaryWindow })"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    :skeleton-rows="8"
    @retry="state.loadBreakdown()"
  >
    <template #actions>
      <label class="flex items-center gap-2 text-xs text-gray-600 dark:text-dark-300">
        {{ t('admin.cacheCharts.breakdown.sort') }}
        <select :value="state.filters.sort" class="input w-40 py-1.5 text-xs" data-test="breakdown-sort" @change="state.setFilter('sort', ($event.target as HTMLSelectElement).value as BreakdownSort)">
          <option v-for="s in sorts" :key="s" :value="s">{{ t(`admin.cacheCharts.breakdown.sorts.${s}`) }}</option>
        </select>
      </label>
    </template>

    <div v-roving-tabs class="tabs mb-4 inline-flex max-w-full flex-wrap" role="tablist" :aria-label="t('admin.cacheCharts.breakdown.dimension')">
      <button
        v-for="d in dimensions"
        :key="d"
        type="button"
        role="tab"
        class="tab"
        :class="state.filters.dimension === d ? 'tab-active' : ''"
        :aria-selected="state.filters.dimension === d"
        :data-test="`dimension-${d}`"
        @click="state.setFilter('dimension', d)"
      >
        {{ t(`admin.cacheCharts.breakdown.dimensions.${d}`) }}
      </button>
    </div>

    <!-- Small entries are merged into `other` (§1.1, fewer than 10 requests); with only that, the table still shows it. -->
    <p v-if="data && !data.rows.length && !data.other" class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.breakdown.empty') }}</p>
    <div v-else-if="data" class="overflow-x-auto" role="tabpanel">
      <table class="w-full min-w-[56rem] text-sm" data-test="breakdown-table">
        <caption class="sr-only">{{ t('admin.cacheCharts.breakdown.caption', { dimension: t(`admin.cacheCharts.breakdown.dimensions.${data.dimension}`) }) }}</caption>
        <thead>
          <tr class="text-left text-xs text-gray-600 dark:text-dark-300">
            <th scope="col" class="sticky left-0 bg-white py-2 pr-3 font-medium dark:bg-dark-800">
              {{ t('admin.cacheCharts.breakdown.name') }}
              <HelpTooltip :content="t('admin.cacheCharts.breakdown.snapshotHelp')" />
            </th>
            <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.requests') }}</th>
            <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.prompt') }}</th>
            <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
            <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.requestHit') }}</th>
            <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.opportunity') }}</th>
            <th scope="col" class="w-40 py-2 pr-3 font-medium">{{ amountSort ? t('admin.cacheCharts.columns.amountShare') : t('admin.cacheCharts.columns.opportunityShare') }}</th>
            <template v-if="hasAmount">
              <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.weightedHit') }}</th>
              <th scope="col" class="py-2 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.saved') }}</th>
              <th scope="col" class="py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.opportunityAmount') }}</th>
            </template>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 tabular-nums dark:divide-dark-700">
          <tr v-for="row in tableRows" :key="row.key" :class="row.other ? 'text-gray-500 dark:text-dark-400' : 'text-gray-800 dark:text-gray-100'">
            <th scope="row" class="sticky left-0 max-w-[16rem] bg-white py-2 pr-3 text-left font-normal dark:bg-dark-800">
              <span class="block truncate" :title="row.label">{{ row.label }}</span>
              <span v-if="row.meta" class="block truncate text-xs text-gray-500 dark:text-dark-400">{{ row.meta }}</span>
            </th>
            <td class="py-2 pr-3 text-right">{{ formatCount(row.requests) }}</td>
            <td class="py-2 pr-3 text-right">{{ formatTokensM(row.prompt) }}</td>
            <td class="py-2 pr-3 text-right">{{ formatInterval(row.hitLower, row.hitUpper) }}</td>
            <td class="py-2 pr-3 text-right">{{ formatPct(row.requestHit, 1) }}</td>
            <td class="py-2 pr-3 text-right">{{ formatTokensM(row.opportunity) }}</td>
            <td class="py-2 pr-3">
              <span class="flex items-center gap-2">
                <span class="h-1.5 rounded-sm bg-primary-600" :style="{ width: `${Math.max(0, Math.min(1, row.share ?? 0)) * 6}rem` }" aria-hidden="true" />
                <span class="text-xs">{{ formatPct(row.share, 1) }}</span>
              </span>
            </td>
            <template v-if="hasAmount">
              <td class="py-2 pr-3 text-right">{{ formatPct(row.weightedHit, 1) }}</td>
              <td class="py-2 pr-3 text-right">{{ formatMoney(row.saved, currency) }}</td>
              <td class="py-2 text-right">{{ formatMoney(row.opportunityAmount, currency) }}</td>
            </template>
          </tr>
        </tbody>
      </table>
    </div>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { vRovingTabs } from '@/utils/rovingTabs'
import type { CacheChartsState } from '../useCacheCharts'
import type { BreakdownDimension, BreakdownMetrics, BreakdownRow, BreakdownSort } from '../types'
import { formatCount, formatInterval, formatMoney, formatPct, formatTokensM } from '../viewModel'
import SectionShell from './SectionShell.vue'

const props = defineProps<{ state: CacheChartsState }>()
const { t } = useI18n()

const dimensions: BreakdownDimension[] = ['group', 'account', 'upstream_host', 'client_family', 'protocol', 'model']
const sorts: BreakdownSort[] = ['opportunity', 'opportunity_amount', 'prompt', 'token_hit']

const section = computed(() => props.state.breakdown)
const data = computed(() => section.value.data)
const amountSort = computed(() => props.state.filters.sort === 'opportunity_amount')
const hasAmount = computed(() => !!data.value && (data.value.rows.some((r) => typeof r.saved_amount === 'number') || typeof data.value.other?.saved_amount === 'number'))
const currency = computed(() => data.value?.cost?.currency ?? null)

function metaText(row: BreakdownRow, dimension: BreakdownDimension, currencyCode: string | null): string {
  const meta = row.meta ?? {}
  const parts: string[] = []
  if (row.key !== row.label) parts.push(`#${row.key}`)
  if (dimension === 'group' && props.state.fallbackGroupIds.value.has(Number(row.key))) parts.push(t('admin.cacheCharts.breakdown.fallback'))
  if (typeof meta.upstream_host === 'string') parts.push(meta.upstream_host)
  if (typeof meta.accounts === 'number') parts.push(t('admin.cacheCharts.breakdown.accounts', { n: meta.accounts }))
  // w_m: list-price value of a million tokens that could have been read (§8.3), not scaled by the cost view.
  if (typeof meta.w_m === 'number') parts.push(t('admin.cacheCharts.breakdown.wm', { value: formatMoney(meta.w_m, currencyCode, 4) }))
  if (meta.priced === false) parts.push(t('admin.cacheCharts.breakdown.unpriced'))
  return parts.join(', ')
}

function toRow(key: string, label: string, meta: string, m: BreakdownMetrics, other: boolean) {
  return {
    key,
    label,
    meta,
    other,
    requests: m.requests,
    prompt: m.prompt_tokens,
    hitLower: m.token_hit_lower,
    hitUpper: m.token_hit_upper,
    requestHit: m.request_hit,
    opportunity: m.opportunity_recorded_tokens,
    share: amountSort.value ? m.opportunity_amount_share ?? null : m.opportunity_share,
    weightedHit: m.token_hit_cost_weighted_upper ?? null,
    saved: m.saved_amount ?? null,
    opportunityAmount: m.opportunity_amount ?? null,
  }
}

const tableRows = computed(() => {
  const d = data.value
  if (!d) return []
  const rows = d.rows.map((r) => toRow(r.key, r.label, metaText(r, d.dimension, currency.value), r, false))
  if (d.other) rows.push(toRow('__other', t('admin.cacheCharts.breakdown.other', { n: d.other.count }), '', d.other, true))
  return rows
})
</script>
