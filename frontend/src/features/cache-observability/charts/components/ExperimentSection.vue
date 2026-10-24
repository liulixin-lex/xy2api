<template>
  <SectionShell
    id="experiments"
    :title="t('admin.cacheCharts.experiment.title')"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    @retry="state.loadExperiments()"
  >
    <div v-if="data && !data.experiments.length" class="rounded-xl bg-gray-50 px-4 py-4 text-sm text-gray-700 dark:bg-dark-900/60 dark:text-dark-200" data-test="experiments-empty">
      <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.cacheCharts.experiment.emptyTitle') }}</p>
      <p class="mt-1 max-w-prose">{{ t('admin.cacheCharts.experiment.emptyBody') }}</p>
    </div>
    <div v-else-if="data" class="space-y-8">
      <article v-for="exp in data.experiments" :key="exp.id" :aria-labelledby="`cache-exp-${exp.id}`" :data-test="`experiment-${exp.id}`">
        <header class="flex flex-wrap items-center gap-x-3 gap-y-1">
          <h3 :id="`cache-exp-${exp.id}`" class="text-sm font-semibold text-gray-900 dark:text-white">{{ exp.id }} {{ exp.lever }} {{ exp.title }}</h3>
          <span class="badge" :class="exp.status === 'running' ? 'badge-primary' : 'badge-gray'">{{ label('status', exp.status) }}</span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.experiment.meta', { build: exp.build_id, phase: exp.phase, rev: exp.policy_rev, start: exp.started_at ? exp.started_at.slice(0, 16).replace('T', ' ') : '-' }) }}</span>
          <span class="ml-auto text-sm font-medium" :class="verdictTone(exp.verdict)">{{ label('verdict', exp.verdict) }}</span>
        </header>

        <div class="mt-3 overflow-x-auto">
          <table class="w-full min-w-[40rem] text-xs">
            <caption class="sr-only">{{ t('admin.cacheCharts.experiment.armsCaption') }}</caption>
            <thead>
              <tr class="text-left text-gray-600 dark:text-dark-300">
                <th scope="col" class="py-1.5 pr-3 font-medium">{{ t('admin.cacheCharts.experiment.arm') }}</th>
                <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.experiment.sessions') }}</th>
                <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
                <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.experiment.underRead') }}</th>
                <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.experiment.switches') }}</th>
                <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.experiment.errors') }}</th>
                <th scope="col" class="py-1.5 text-right font-medium">{{ t('admin.cacheCharts.experiment.undelivered') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 tabular-nums text-gray-800 dark:divide-dark-700 dark:text-gray-100">
              <tr v-for="arm in exp.arms" :key="arm.arm">
                <th scope="row" class="py-1.5 pr-3 text-left font-normal">{{ label('armNames', arm.arm) }}</th>
                <td class="py-1.5 pr-3 text-right">{{ formatCount(arm.sessions) }} / {{ formatCount(exp.planned_per_arm) }}</td>
                <td class="py-1.5 pr-3 text-right">{{ formatPct(arm.token_hit) }}</td>
                <td class="py-1.5 pr-3 text-right">{{ formatPct(arm.under_read_share, 1) }}</td>
                <td class="py-1.5 pr-3 text-right">{{ arm.switches_per_100 ?? '-' }}</td>
                <td class="py-1.5 pr-3 text-right">{{ arm.errors === null || arm.attempts === null ? '-' : `${formatCount(arm.errors)} / ${formatCount(arm.attempts)}` }}</td>
                <td class="py-1.5 text-right">{{ arm.undelivered === null ? '-' : formatCount(arm.undelivered) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mt-4">
          <h4 class="text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.experiment.slicesTitle') }}</h4>
          <ul class="mt-2 space-y-2" role="list">
            <li v-for="s in exp.slices" :key="s.slice" class="grid grid-cols-[8rem_minmax(0,1fr)_11rem] items-center gap-3 text-xs" :data-slice="s.slice">
              <span class="text-gray-700 dark:text-dark-200">{{ label('slices', s.slice) }}</span>
              <span class="relative h-5" aria-hidden="true">
                <span v-for="line in references" :key="line" class="absolute inset-y-0 w-px" :class="line === 0 ? 'bg-gray-500 dark:bg-dark-300' : 'bg-gray-200 dark:bg-dark-600'" :style="{ left: `${pos(line, exp)}%` }" />
                <template v-if="s.ci_ready && s.ci_low_pp !== null && s.ci_high_pp !== null && s.diff_pp !== null">
                  <span class="absolute top-1/2 h-0.5 -translate-y-1/2 bg-primary-600" :style="{ left: `${pos(s.ci_low_pp, exp)}%`, width: `${pos(s.ci_high_pp, exp) - pos(s.ci_low_pp, exp)}%` }" />
                  <span class="absolute top-1/2 h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-primary-600 ring-2 ring-white dark:ring-dark-800" :style="{ left: `${pos(s.diff_pp, exp)}%` }" />
                </template>
              </span>
              <span class="tabular-nums text-gray-800 dark:text-gray-100">
                <template v-if="s.ci_ready">{{ formatPp(s.diff_pp) }} [{{ bare(s.ci_low_pp) }}, {{ bare(s.ci_high_pp) }}]</template>
                <template v-else>{{ t('admin.cacheCharts.experiment.ciNotReady') }}</template>
                <span class="ml-1 text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.experiment.n', { off: s.sessions_off, on: s.sessions_on }) }}</span>
              </span>
            </li>
          </ul>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.experiment.axisNote') }}</p>
        </div>

        <ul class="mt-4 space-y-1" role="list">
          <li v-for="c in sortedCriteria(exp.criteria)" :key="c.id" class="flex items-start gap-2 text-xs" :class="criterionTone(c)">
            <Icon :name="c.met === null ? 'clock' : c.met ? 'checkCircle' : 'xCircle'" size="sm" class="shrink-0" aria-hidden="true" />
            <span>
              {{ label('criteria', c.id) }}
              <span class="ml-1 font-medium">{{ c.met === null ? t('admin.cacheCharts.experiment.pending') : c.met ? t('admin.cacheCharts.experiment.met') : t('admin.cacheCharts.experiment.notMet') }}</span>
            </span>
          </li>
        </ul>
      </article>
    </div>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { CacheChartsState } from '../useCacheCharts'
import type { Experiment, ExperimentCriterion } from '../types'
import { formatCount, formatPct, formatPp } from '../viewModel'
import SectionShell from './SectionShell.vue'

const props = defineProps<{ state: CacheChartsState }>()
const { t, te } = useI18n()

const section = computed(() => props.state.experiments)
const data = computed(() => section.value.data)
// DESIGN §3.7: 0 and the −3 / −2 / +3 decision lines.
const references = [-3, -2, 0, 3]

function label(group: string, code: string): string {
  const key = `admin.cacheCharts.experiment.${group}.${code}`
  return te(key) ? t(key) : code
}
function bare(value: number | null): string {
  return formatPp(value).replace(/pp$/, '')
}
function range(exp: Experiment): [number, number] {
  let lo = -5
  let hi = 8
  for (const s of exp.slices) {
    if (typeof s.ci_low_pp === 'number') lo = Math.min(lo, Math.floor(s.ci_low_pp) - 1)
    if (typeof s.ci_high_pp === 'number') hi = Math.max(hi, Math.ceil(s.ci_high_pp) + 1)
  }
  return [lo, hi]
}
function pos(value: number, exp: Experiment): number {
  const [lo, hi] = range(exp)
  return ((value - lo) / (hi - lo)) * 100
}
function sortedCriteria(list: ExperimentCriterion[]): ExperimentCriterion[] {
  // A met stop condition goes first: it is the one thing that needs action.
  return [...list].sort((a, b) => Number(b.id.startsWith('stop_') && b.met === true) - Number(a.id.startsWith('stop_') && a.met === true))
}
function criterionTone(c: ExperimentCriterion): string {
  if (c.id.startsWith('stop_') && c.met === true) return 'font-semibold text-red-700 dark:text-red-300'
  if (c.met === true) return 'text-emerald-700 dark:text-emerald-300'
  return 'text-gray-700 dark:text-dark-200'
}
function verdictTone(verdict: string): string {
  if (verdict === 'stopped' || verdict === 'needs_review') return 'text-red-700 dark:text-red-300'
  if (verdict === 'positive' || verdict === 'safe_only') return 'text-emerald-700 dark:text-emerald-300'
  return 'text-gray-700 dark:text-dark-200'
}
</script>
