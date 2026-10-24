<template>
  <SectionShell
    id="northstar"
    :title="t('admin.cacheCharts.northstar.title')"
    :subtitle="t('admin.cacheCharts.northstar.subtitle', { window: state.filters.summaryWindow })"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    :skeleton-rows="6"
    @retry="state.loadNorthStar()"
  >
    <div v-if="data" class="grid gap-6 lg:grid-cols-[minmax(16rem,1fr)_3fr]">
      <div class="min-w-0">
        <p class="flex items-center gap-1 text-xs text-gray-600 dark:text-dark-300">
          {{ t('admin.cacheCharts.northstar.tokenHit') }}
          <HelpTooltip :content="t('admin.cacheCharts.northstar.intervalHelp')" />
        </p>
        <p class="mt-1 text-5xl font-semibold leading-tight text-gray-900 dark:text-white" data-test="northstar-hero">
          {{ formatInterval(summary?.token_hit_lower, summary?.token_hit_upper) }}
        </p>
        <p v-if="delta !== null" class="mt-1 flex items-center gap-1 text-sm" :class="delta < 0 ? 'text-red-700 dark:text-red-300' : 'text-emerald-700 dark:text-emerald-300'" data-test="northstar-delta">
          <Icon :name="delta < 0 ? 'arrowDown' : 'arrowUp'" size="xs" aria-hidden="true" />
          {{ t('admin.cacheCharts.northstar.delta', { delta: formatPp(delta), window: state.filters.summaryWindow }) }}
        </p>
        <!-- Contract R5: the previous window may have no ratio to compare with; say so instead of hiding the line. -->
        <p v-else-if="summary?.previous" class="mt-1 text-sm text-gray-500 dark:text-dark-400" data-test="northstar-delta">
          {{ t('admin.cacheCharts.northstar.delta', { delta: formatPp(null), window: state.filters.summaryWindow }) }}
        </p>

        <dl class="mt-5 space-y-2 text-sm">
          <div class="flex justify-between gap-4">
            <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.northstar.requestHit') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatPct(summary?.request_hit) }}</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.northstar.opportunity') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatTokensM(summary?.opportunity_recorded_tokens) }}</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.northstar.coverage') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatPct(summary?.business_coverage, 1) }}</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.northstar.standardized') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">
              {{ formatPct(summary?.standardized_hit) }}
              <span v-if="partialWeights" class="ml-1 text-xs text-amber-800 dark:text-amber-300">{{ t('admin.cacheCharts.northstar.partialWeights') }}</span>
            </dd>
          </div>
        </dl>

        <div class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700" data-test="northstar-cost">
          <p class="text-xs font-semibold text-gray-700 dark:text-dark-200">
            {{ t('admin.cacheCharts.cost.title', { view: t(`admin.cacheCharts.costViews.${state.filters.costView}`) }) }}
          </p>
          <p v-if="!cost" class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.cost.unavailable') }}</p>
          <template v-else>
            <dl class="mt-2 space-y-2 text-sm">
              <div class="flex justify-between gap-4">
                <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.cost.weightedHit') }}</dt>
                <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatInterval(cost.token_hit_cost_weighted_lower, cost.token_hit_cost_weighted_upper) }}</dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.cost.saved') }}</dt>
                <dd class="tabular-nums text-gray-900 dark:text-gray-100">
                  {{ formatMoney(cost.saved_amount, cost.currency) }}
                  <span v-if="savedDelta !== null" class="ml-1 text-xs text-gray-500 dark:text-dark-400">{{ formatSignedMoney(savedDelta, cost.currency) }}</span>
                </dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.cost.opportunity') }}</dt>
                <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatMoney(cost.opportunity_amount, cost.currency) }}</dd>
              </div>
            </dl>
            <p class="mt-2 text-xs text-gray-600 dark:text-dark-300" data-test="cost-basis">
              {{ t('admin.cacheCharts.cost.basis', { n: cost.priced_models ?? '-', share: formatPct(cost.priced_p_share, 1) }) }}
              <template v-if="typeof cost.upstream_covered_p_share === 'number'">{{ t('admin.cacheCharts.cost.upstreamCovered', { share: formatPct(cost.upstream_covered_p_share, 1) }) }}</template>
            </p>
            <ul v-if="costNotes.length" class="mt-2 space-y-1" role="list" data-test="cost-notes">
              <li v-for="note in costNotes" :key="note" class="flex items-start gap-1.5 text-xs text-amber-800 dark:text-amber-300">
                <Icon name="exclamationTriangle" size="xs" class="mt-0.5 shrink-0" aria-hidden="true" />
                {{ note }}
              </li>
            </ul>
          </template>
        </div>
      </div>

      <NorthStarTrend :series="data.series" :events="data.events" :granularity="data.granularity" :currency="cost?.currency ?? null" />
    </div>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { CacheChartsState } from '../useCacheCharts'
import { formatInterval, formatMoney, formatPct, formatPp, formatSignedMoney, formatTokensM, ratioDeltaPp } from '../viewModel'
import NorthStarTrend from './NorthStarTrend.vue'
import SectionShell from './SectionShell.vue'

const props = defineProps<{ state: CacheChartsState }>()
const { t, te } = useI18n()

const section = computed(() => props.state.northstar)
const data = computed(() => section.value.data)
const summary = computed(() => data.value?.summary ?? null)
const cost = computed(() => summary.value?.cost ?? null)
const delta = computed(() => ratioDeltaPp(summary.value?.token_hit_upper, summary.value?.previous?.token_hit_upper))
const partialWeights = computed(() => typeof summary.value?.standardized_weight_coverage === 'number' && summary.value.standardized_weight_coverage < 1)
const savedDelta = computed(() => {
  const c = cost.value
  return c && typeof c.saved_amount === 'number' && typeof c.previous?.saved_amount === 'number' ? c.saved_amount - c.previous.saved_amount : null
})

// §8.2 notes vocabulary; the boolean pricing flag and its note render once. Unknown codes show as sent.
const costNotes = computed(() => {
  const c = cost.value
  if (!c) return []
  if (c.priced_p_share === 0) return [t('admin.cacheCharts.cost.noPriced')]
  const codes = new Set(c.notes)
  if (c.pricing_changed_in_window === true) codes.add('pricing_changed_in_window')
  const params = { share: formatPct(c.priced_p_share, 1), covered: formatPct(c.upstream_covered_p_share, 1) }
  return [...codes].map((code) => {
    const key = `admin.cacheCharts.cost.notes.${code}`
    return te(key) ? t(key, params) : code
  })
})
</script>
