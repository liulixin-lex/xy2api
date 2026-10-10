<template>
  <div class="space-y-5" data-testid="share-report">
    <div class="card flex flex-wrap items-end justify-between gap-3 p-4">
      <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.report.window') }}
        <select v-model="reportWindow" class="input mt-1 w-40" data-testid="share-report-window">
          <option v-for="option in windows" :key="option.value" :value="option.value">{{ t('admin.shareScheduling.report.windows.' + option.key) }}</option>
        </select>
      </label>
      <button type="button" class="btn btn-secondary" :disabled="loading" data-testid="share-report-refresh" @click="load">{{ loading ? t('common.loading') : t('admin.shareScheduling.report.refresh') }}</button>
    </div>

    <p v-if="failed" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ failed }}</p>
    <div v-if="loading && !report" class="grid gap-3 sm:grid-cols-4" role="status" :aria-label="t('common.loading')">
      <div v-for="cell in 4" :key="cell" class="h-20 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-700" />
    </div>

    <template v-if="report">
      <p v-if="report.counters_error" role="alert" class="rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ t('admin.shareScheduling.report.countersError', { error: report.counters_error }) }}</p>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="card in cards" :key="card.key" class="card p-4">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.report.cards.' + card.key) }}</p>
          <p class="mt-1 text-xl font-semibold text-gray-900 dark:text-white" :data-testid="'share-card-' + card.key">{{ card.value }}</p>
        </div>
      </div>
      <p class="text-xs text-gray-500 dark:text-dark-400">
        {{ t('admin.shareScheduling.report.sampledNote', { rate: sampleRate ?? 4 }) }}
        <template v-if="report.decisions.dropped + report.cache.dropped > 0"> {{ t('admin.shareScheduling.report.droppedNote', { count: report.decisions.dropped + report.cache.dropped }) }}</template>
      </p>

      <section class="card p-5">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.report.shares.title') }}</h3>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.report.shares.unitNote', { unit: t('admin.shareScheduling.policy.options.unit.' + report.unit) }) }}</span>
        </div>
        <div class="mt-3 overflow-x-auto">
          <table class="w-full min-w-[640px] text-left text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.policy.columns.account') }}</th>
                <th class="w-1/3 p-2">{{ t('admin.shareScheduling.report.shares.target') }} / {{ t('admin.shareScheduling.report.shares.actual') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.shares.target') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.shares.actual') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.shares.deviation') }}</th>
                <th v-if="report.corrections" class="p-2 text-right">{{ t('admin.shareScheduling.report.shares.correction') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in shareRows" :key="row.id" class="border-b border-gray-100 last:border-0 dark:border-dark-700" :data-testid="'share-report-row-' + row.id">
                <td class="p-2">
                  <div class="font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
                  <div class="text-xs text-gray-500 dark:text-dark-400">#{{ row.id }} · T{{ row.tier }}</div>
                </td>
                <td class="p-2">
                  <div class="h-2 rounded-full bg-gray-100 dark:bg-dark-700"><div class="h-2 rounded-full bg-gray-400 dark:bg-dark-400" :style="{ width: row.target * 100 + '%' }" /></div>
                  <div class="mt-1 h-2 rounded-full bg-gray-100 dark:bg-dark-700"><div class="h-2 rounded-full bg-primary-500" :style="{ width: row.actual * 100 + '%' }" /></div>
                </td>
                <td class="p-2 text-right font-mono">{{ formatSharePercent(row.target) }}</td>
                <td class="p-2 text-right font-mono">{{ formatSharePercent(row.actual) }}</td>
                <td class="p-2 text-right font-mono" :class="Math.abs(row.actual - row.target) > 0.05 ? 'text-amber-700 dark:text-amber-300' : 'text-gray-600 dark:text-dark-300'">{{ signedPercent(row.actual - row.target) }}</td>
                <td v-if="report.corrections" class="p-2 text-right font-mono">{{ report.corrections[String(row.id)]?.toFixed(2) ?? '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card p-5">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.report.accounts.title') }}</h3>
        <div class="mt-3 overflow-x-auto">
          <table class="w-full min-w-[640px] text-left text-sm" data-testid="share-account-health">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.policy.columns.account') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.accounts.placed') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.accounts.skipped') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.accounts.skipRate') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.accounts.ttftCount') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.p50') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.p95') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in accountRows" :key="row.id" class="border-b border-gray-100 last:border-0 dark:border-dark-700" :data-testid="'share-account-' + row.id">
                <td class="p-2">
                  <div class="font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
                  <div class="text-xs text-gray-500 dark:text-dark-400">#{{ row.id }}</div>
                </td>
                <td class="p-2 text-right font-mono">{{ formatNumber(row.placed) }}</td>
                <td class="p-2 text-right">
                  <span class="font-mono">{{ formatNumber(row.skipped) }}</span>
                  <span v-if="row.skips.length" class="block text-xs text-gray-500 dark:text-dark-400">{{ row.skips.map(([reason, n]) => labels.reason(reason) + ' ' + formatNumber(n)).join(' · ') }}</span>
                </td>
                <td class="p-2 text-right font-mono" :class="row.busy ? 'text-amber-700 dark:text-amber-300' : 'text-gray-600 dark:text-dark-300'">{{ row.placed + row.skipped > 0 ? formatSharePercent(row.skipRate) : '-' }}</td>
                <td class="p-2 text-right font-mono">{{ row.ttft ? formatNumber(row.ttft.count) : '-' }}</td>
                <td class="p-2 text-right font-mono">{{ row.ttft ? bucket(row.ttft.p50_ms) : '-' }}</td>
                <td class="p-2 text-right font-mono">{{ row.ttft ? bucket(row.ttft.p95_ms) : '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="accountRows.some(row => row.busy)" class="mt-2 text-xs text-amber-700 dark:text-amber-300" data-testid="share-account-busy">{{ t('admin.shareScheduling.report.accounts.busyHint') }}</p>
      </section>

      <section class="card grid gap-6 p-5 md:grid-cols-2">
        <ShareBarList :title="t('admin.shareScheduling.report.reasons')" :values="report.decisions.reasons" :label="labels.reason" :empty="t('admin.shareScheduling.report.noData')" />
        <ShareBarList :title="t('admin.shareScheduling.report.cacheClasses')" :values="report.cache.class_tokens" :label="labels.cacheClass" :empty="t('admin.shareScheduling.report.noData')" bar-class="bg-emerald-500" />
        <ShareBarList :title="t('admin.shareScheduling.report.keySources')" :values="report.decisions.key_sources" :label="labels.keySource" :empty="t('admin.shareScheduling.report.noData')" bar-class="bg-sky-500" />
        <ShareBarList :title="t('admin.shareScheduling.report.idealSkips')" :values="report.decisions.ideal_skips" :label="labels.reason" :empty="t('admin.shareScheduling.report.noData')" bar-class="bg-amber-500" />
        <ShareBarList :title="t('admin.shareScheduling.report.hotReasons')" :values="report.decisions.hot_reasons" :label="labels.reason" :empty="t('admin.shareScheduling.report.noData')" bar-class="bg-red-500" />
        <ShareBarList v-if="report.decisions.shadow > 0" :title="t('admin.shareScheduling.report.shadowReasons')" :values="report.decisions.shadow_reasons" :label="labels.reason" :empty="t('admin.shareScheduling.report.noData')" bar-class="bg-violet-500" />
      </section>

      <section class="card p-5">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.report.ttft') }}</h3>
        <div v-if="ttftRows.length" class="mt-3 overflow-x-auto">
          <table class="w-full text-left text-sm" data-testid="share-ttft">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.report.ttftColumns.reason') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.count') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.p50') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.p95') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.ttftColumns.p99') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in ttftRows" :key="row.reason" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                <td class="p-2">{{ labels.reason(row.reason) }}</td>
                <td class="p-2 text-right font-mono">{{ formatNumber(row.count) }}</td>
                <td class="p-2 text-right font-mono">{{ bucket(row.p50_ms) }}</td>
                <td class="p-2 text-right font-mono">{{ bucket(row.p95_ms) }}</td>
                <td class="p-2 text-right font-mono">{{ bucket(row.p99_ms) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.report.noData') }}</p>
      </section>

      <section class="card p-5">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.report.decisions') }}</h3>
        <div v-if="decisions.length" class="mt-3 max-h-[480px] overflow-auto">
          <table class="w-full min-w-[720px] text-left text-xs" data-testid="share-decisions">
            <thead class="sticky top-0 bg-white dark:bg-dark-800">
              <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.report.decisionColumns.time') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.report.decisionColumns.reason') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.report.decisionColumns.model') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.report.decisionColumns.accounts') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.decisionColumns.wait') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.report.decisionColumns.idle') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.report.decisionColumns.flags') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(sample, index) in decisions" :key="index" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                <td class="whitespace-nowrap p-2 text-gray-500 dark:text-dark-400">{{ formatTime(sample.at) }}</td>
                <td class="p-2">
                  <span class="text-gray-900 dark:text-white">{{ labels.reason(sample.reason) }}</span>
                  <span v-if="sample.ideal_skip" class="block text-gray-500 dark:text-dark-400">{{ labels.reason(sample.ideal_skip) }}</span>
                </td>
                <td class="p-2">{{ sample.model || '-' }}<span class="block text-gray-500 dark:text-dark-400">{{ labels.keySource(sample.key_source) }}</span></td>
                <td class="whitespace-nowrap p-2 font-mono">{{ sample.bound ? '#' + sample.bound : '-' }} → {{ sample.served ? '#' + sample.served : '-' }}<span v-if="sample.original" class="block font-sans text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.report.original', { id: sample.original }) }}</span></td>
                <td class="p-2 text-right font-mono">{{ sample.wait_ms ? formatShareMs(sample.wait_ms) : '-' }}</td>
                <td class="p-2 text-right font-mono">{{ sample.idle_ms ? formatShareMs(sample.idle_ms) : '-' }}</td>
                <td class="p-2">
                  <span v-if="sample.hot_migration" class="mr-1 rounded bg-red-50 px-1 text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ t('admin.shareScheduling.report.hot') }}</span>
                  <span v-if="sample.queued" class="mr-1 rounded bg-amber-50 px-1 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300">{{ t('admin.shareScheduling.report.queued') }}</span>
                  <span v-if="sample.agree === true" class="rounded bg-emerald-50 px-1 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300">{{ t('admin.shareScheduling.report.agree') }}</span>
                  <span v-else-if="sample.agree === false" class="rounded bg-violet-50 px-1 text-violet-700 dark:bg-violet-900/20 dark:text-violet-300">{{ t('admin.shareScheduling.report.disagree') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.report.noDecisions') }}</p>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ShareBarList from './ShareBarList.vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type { ShareDecisionSample, ShareReport } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatNumber, formatTime } from '@/utils/format'
import { formatShareMs, formatSharePercent } from '@/utils/shareScheduling'

const props = defineProps<{
  groupId: number
  sampleRate?: number
}>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const windows = [
  { key: 'm15', value: '15m' },
  { key: 'h1', value: '1h' },
  { key: 'h6', value: '6h' },
  { key: 'h24', value: '24h' }
]
const reportWindow = ref('1h')
const report = ref<ShareReport | null>(null)
const decisions = ref<ShareDecisionSample[]>([])
const loading = ref(false)
const failed = ref('')
let request = 0

const actualKey = computed(() => {
  switch (report.value?.unit) {
    case 'requests': return 'request_share'
    case 'tokens': return 'token_share'
    case 'standard_cost': return 'cost_share'
    default: return 'placed_share'
  }
})

const shareRows = computed(() => {
  if (!report.value) return []
  const actual = new Map(report.value.actual.map(a => [a.account_id, a]))
  return report.value.members.map(m => ({
    id: m.account_id,
    name: m.name,
    tier: m.tier,
    target: m.effective_share,
    actual: actual.get(m.account_id)?.[actualKey.value] ?? 0
  }))
})

// A tenth of tries is noise; a fifth passed over means the weight asks
// for more than the account's concurrency takes.
const BUSY_MIN_TRIES = 10
const BUSY_SKIP_RATE = 0.2

const accountRows = computed(() => {
  if (!report.value) return []
  const actual = new Map(report.value.actual.map(a => [a.account_id, a]))
  return report.value.members.map(m => {
    const a = actual.get(m.account_id)
    const skips = Object.entries(a?.skipped ?? {}).sort((x, y) => y[1] - x[1])
    const skipped = skips.reduce((sum, [, n]) => sum + n, 0)
    const placed = a?.placed ?? 0
    const tries = placed + skipped
    const skipRate = tries > 0 ? skipped / tries : 0
    return { id: m.account_id, name: m.name, placed, skipped, skips, skipRate, busy: tries >= BUSY_MIN_TRIES && skipRate >= BUSY_SKIP_RATE, ttft: a?.ttft }
  })
})

const cards = computed(() => {
  const r = report.value
  if (!r) return []
  const d = r.decisions
  const out = [
    { key: 'requests', value: formatNumber(d.requests) },
    { key: 'cacheRead', value: formatSharePercent(r.cache.read_ratio) },
    { key: 'schedulingMiss', value: formatSharePercent(r.cache.scheduling_miss_ratio) },
    { key: 'hotMigrations', value: formatNumber(d.hot_migrations) },
    { key: 'waitHit', value: d.wait_count > 0 ? `${formatNumber(d.wait_hit)} / ${formatNumber(d.wait_count)}` : '-' },
    { key: 'avgWait', value: d.wait_count > 0 ? formatShareMs(d.avg_wait_ms) : '-' },
    { key: 'attempts', value: formatNumber(d.attempts) }
  ]
  if (d.shadow > 0) out.push({ key: 'shadowAgree', value: formatSharePercent(d.shadow_agree_rate) })
  return out
})

const ttftRows = computed(() => Object.entries(report.value?.cache.ttft ?? {})
  .filter(([, value]) => value.count > 0)
  .map(([reason, value]) => ({ reason, ...value }))
  .sort((a, b) => b.count - a.count))

function bucket(ms: number | null): string {
  return ms === null ? '> 30 s' : `≤ ${formatShareMs(ms)}`
}

function signedPercent(value: number): string {
  const text = formatSharePercent(Math.abs(value))
  if (Math.abs(value) < 0.0005) return text
  return (value > 0 ? '+' : '−') + text
}

async function load() {
  const current = ++request
  loading.value = true
  failed.value = ''
  try {
    const [nextReport, nextDecisions] = await Promise.all([
      shareSchedulingAPI.report(props.groupId, reportWindow.value),
      shareSchedulingAPI.decisions(props.groupId, 100).catch(() => [] as ShareDecisionSample[])
    ])
    if (current !== request) return
    report.value = nextReport
    decisions.value = nextDecisions
  } catch (err) {
    if (current !== request) return
    failed.value = extractApiErrorMessage(err, t('admin.shareScheduling.report.loadFailed'))
  } finally {
    if (current === request) loading.value = false
  }
}

watch(() => props.groupId, () => {
  report.value = null
  decisions.value = []
  void load()
})
watch(reportWindow, () => { void load() })
onMounted(load)
</script>
