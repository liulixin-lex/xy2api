<template>
  <div class="flex min-w-0 flex-col" data-test="northstar-trend">
    <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
      <div class="tabs inline-flex" role="radiogroup" :aria-label="t('admin.cacheCharts.trend.mode')">
        <button
          v-for="m in modes"
          :key="m"
          type="button"
          role="radio"
          class="tab"
          :class="mode === m ? 'tab-active' : ''"
          :aria-checked="mode === m"
          :disabled="m === 'amount' && !hasAmount"
          @click="mode = m"
        >
          {{ t(`admin.cacheCharts.trend.modes.${m}`) }}
        </button>
      </div>
      <div class="flex items-center gap-3">
        <ul class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-dark-300" :aria-label="t('admin.cacheCharts.trend.legend')">
          <li v-for="item in legend" :key="item.label" class="flex items-center gap-1.5">
            <span class="inline-block h-0.5 w-4 rounded" :style="{ backgroundColor: item.color }" aria-hidden="true" />
            {{ item.label }}
          </li>
          <li v-if="markers.length" class="flex items-center gap-1.5">
            <span class="inline-block h-3 w-px bg-gray-500 dark:bg-dark-300" aria-hidden="true" />
            {{ t('admin.cacheCharts.trend.deploys') }}
          </li>
        </ul>
        <button type="button" class="btn btn-ghost btn-sm" :aria-pressed="showTable" @click="showTable = !showTable">
          <Icon :name="showTable ? 'chartBar' : 'document'" size="sm" aria-hidden="true" />
          {{ showTable ? t('admin.cacheCharts.trend.showChart') : t('admin.cacheCharts.trend.showTable') }}
        </button>
      </div>
    </div>

    <div v-if="!showTable" class="relative h-64 min-w-0">
      <Line v-if="chartData" :data="chartData" :options="options" :plugins="[deployRuler]" role="img" :aria-label="ariaLabel" />
      <p v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.trend.empty') }}</p>
    </div>
    <div v-else class="max-h-64 overflow-auto rounded-xl ring-1 ring-gray-100 dark:ring-dark-700" data-test="trend-table">
      <table class="w-full text-xs">
        <caption class="sr-only">{{ t('admin.cacheCharts.trend.tableCaption') }}</caption>
        <thead class="sticky top-0 bg-gray-50 dark:bg-dark-900">
          <tr class="text-left text-gray-600 dark:text-dark-300">
            <th scope="col" class="px-3 py-2 font-medium">{{ t('admin.cacheCharts.trend.time') }}</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.requests') }}</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.requestHit') }}</th>
            <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.opportunity') }}</th>
            <th v-if="hasAmount" scope="col" class="px-3 py-2 text-right font-medium">{{ t('admin.cacheCharts.columns.saved') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 tabular-nums dark:divide-dark-700">
          <tr v-for="p in [...series].reverse()" :key="p.bucket_start" class="text-gray-800 dark:text-gray-100">
            <th scope="row" class="whitespace-nowrap px-3 py-1.5 text-left font-normal">{{ label(p.bucket_start) }}<span v-if="p.partial" class="ml-1 text-gray-500">{{ t('admin.cacheCharts.trend.partial') }}</span></th>
            <td class="px-3 py-1.5 text-right">{{ formatCount(p.requests) }}</td>
            <td class="px-3 py-1.5 text-right">{{ formatInterval(p.token_hit_lower, p.token_hit_upper) }}</td>
            <td class="px-3 py-1.5 text-right">{{ formatPct(p.request_hit) }}</td>
            <td class="px-3 py-1.5 text-right">{{ formatTokensM(p.opportunity_recorded_tokens) }}</td>
            <td v-if="hasAmount" class="px-3 py-1.5 text-right">{{ formatMoney(p.saved_amount, currency) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CategoryScale, Chart as ChartJS, Filler, LinearScale, LineElement, PointElement, Tooltip, type Plugin } from 'chart.js'
import { Line } from 'vue-chartjs'
import Icon from '@/components/icons/Icon.vue'
import type { Granularity, NorthStarPoint, PanelEvent } from '../types'
import { bucketIndex, formatCount, formatInterval, formatMoney, formatPct, formatTokensM, TREND_COLORS } from '../viewModel'

ChartJS.register(Tooltip, LineElement, LinearScale, PointElement, CategoryScale, Filler)

const props = defineProps<{ series: NorthStarPoint[]; events: PanelEvent[]; granularity: Granularity; currency: string | null }>()
const { t } = useI18n()

type Mode = 'rate' | 'amount'
const modes: Mode[] = ['rate', 'amount']
const mode = ref<Mode>('rate')
const showTable = ref(false)
const dark = computed(() => document.documentElement.classList.contains('dark'))
const hasAmount = computed(() => props.series.some((p) => typeof p.saved_amount === 'number'))

const palette = computed(() => ({
  token: dark.value ? TREND_COLORS.tokenHit.dark : TREND_COLORS.tokenHit.light,
  request: dark.value ? TREND_COLORS.requestHit.dark : TREND_COLORS.requestHit.light,
  grid: dark.value ? '#334155' : '#f3f4f6',
  text: dark.value ? '#cbd5e1' : '#4b5563',
  ruler: dark.value ? '#cbd5e1' : '#6b7280',
  cold: dark.value ? 'rgba(148, 163, 184, 0.14)' : 'rgba(107, 114, 128, 0.10)',
  surface: dark.value ? '#1e293b' : '#ffffff',
}))

function label(iso: string): string {
  const d = new Date(iso)
  const md = `${String(d.getUTCMonth() + 1).padStart(2, '0')}-${String(d.getUTCDate()).padStart(2, '0')}`
  return props.granularity === '1d' ? md : `${md} ${String(d.getUTCHours()).padStart(2, '0')}:00`
}

const legend = computed(() =>
  mode.value === 'rate'
    ? [
        { label: t('admin.cacheCharts.trend.tokenHit'), color: palette.value.token },
        { label: t('admin.cacheCharts.trend.requestHit'), color: palette.value.request },
      ]
    : [{ label: t('admin.cacheCharts.trend.saved'), color: palette.value.token }],
)

interface Marker {
  index: number
  coldEnd: number
  label: string
}

const markers = computed<Marker[]>(() => {
  const starts = props.series.map((p) => p.bucket_start)
  return props.events
    .map((ev) => {
      const index = bucketIndex(ev.at, starts)
      const coldEnd = ev.cold_until ? bucketIndex(ev.cold_until, starts) : -1
      const text = ev.kind === 'restart' ? t('admin.cacheCharts.trend.restart', { label: ev.label }) : ev.label
      return { index, coldEnd, label: text }
    })
    .filter((m) => m.index >= 0)
})

const deployRuler: Plugin<'line'> = {
  id: 'cacheDeployRuler',
  beforeDatasetsDraw(chart) {
    const { ctx, chartArea, scales } = chart
    const x = scales.x
    ctx.save()
    ctx.fillStyle = palette.value.cold
    for (const m of markers.value) {
      if (m.coldEnd < m.index) continue
      const left = x.getPixelForValue(m.index)
      const right = x.getPixelForValue(m.coldEnd) + 2
      ctx.fillRect(left, chartArea.top, Math.max(2, right - left), chartArea.bottom - chartArea.top)
    }
    ctx.restore()
  },
  afterDatasetsDraw(chart) {
    const { ctx, chartArea, scales } = chart
    const x = scales.x
    ctx.save()
    ctx.strokeStyle = palette.value.ruler
    ctx.fillStyle = palette.value.text
    ctx.lineWidth = 1
    ctx.font = '10px system-ui, -apple-system, sans-serif'
    let lastLabelRight = -Infinity
    for (const m of markers.value) {
      const px = Math.round(x.getPixelForValue(m.index)) + 0.5
      ctx.beginPath()
      ctx.moveTo(px, chartArea.top)
      ctx.lineTo(px, chartArea.bottom)
      ctx.stroke()
      // Skip a label that would collide with the previous one; the line and the table still carry it.
      const width = ctx.measureText(m.label).width
      if (px + 4 > lastLabelRight + 6 && px + 4 + width < chartArea.right) {
        ctx.fillText(m.label, px + 4, chartArea.top + 10)
        lastLabelRight = px + 4 + width
      }
    }
    ctx.restore()
  },
}

const chartData = computed(() => {
  if (!props.series.length) return null
  const labels = props.series.map((p) => label(p.bucket_start))
  const c = palette.value
  const partialRadius = props.series.map((p) => (p.partial ? 3 : 0))
  if (mode.value === 'amount') {
    return {
      labels,
      datasets: [
        {
          label: t('admin.cacheCharts.trend.saved'),
          data: props.series.map((p) => p.saved_amount ?? null),
          borderColor: c.token,
          backgroundColor: `${c.token}1a`,
          fill: true,
          borderWidth: 2,
          tension: 0.25,
          pointRadius: partialRadius,
          pointBackgroundColor: c.surface,
          pointHitRadius: 10,
        },
      ],
    }
  }
  return {
    labels,
    datasets: [
      {
        label: t('admin.cacheCharts.trend.tokenHitUpper'),
        data: props.series.map((p) => p.token_hit_upper),
        borderColor: c.token,
        backgroundColor: `${c.token}1a`,
        fill: '+1',
        borderWidth: 2,
        tension: 0.25,
        pointRadius: partialRadius,
        pointBackgroundColor: c.surface,
        pointHitRadius: 10,
      },
      {
        label: t('admin.cacheCharts.trend.tokenHitLower'),
        data: props.series.map((p) => p.token_hit_lower),
        borderColor: `${c.token}66`,
        borderWidth: 1,
        tension: 0.25,
        pointRadius: 0,
        fill: false,
      },
      {
        label: t('admin.cacheCharts.trend.requestHit'),
        data: props.series.map((p) => p.request_hit),
        borderColor: c.request,
        borderWidth: 2,
        tension: 0.25,
        pointRadius: 0,
        pointHitRadius: 10,
        fill: false,
      },
    ],
  }
})

const options = computed(() => {
  const c = palette.value
  const amount = mode.value === 'amount'
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: false as const,
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: { display: false },
      tooltip: {
        backgroundColor: dark.value ? '#0f172a' : '#ffffff',
        titleColor: dark.value ? '#f1f5f9' : '#111827',
        bodyColor: dark.value ? '#cbd5e1' : '#374151',
        borderColor: c.grid,
        borderWidth: 1,
        padding: 10,
        callbacks: {
          label: (context: { dataset: { label?: string }; parsed: { y: number | null } }) => {
            const y = context.parsed.y
            const value = amount ? formatMoney(y, props.currency) : formatPct(y)
            return `${context.dataset.label ?? ''}: ${value}`
          },
        },
      },
    },
    scales: {
      x: {
        type: 'category' as const,
        grid: { display: false },
        // Bucket labels are UTC, as in the table's time column.
        title: { display: true, text: t('admin.cacheCharts.trend.time'), color: c.text, font: { size: 10 } },
        ticks: { color: c.text, font: { size: 10 }, maxTicksLimit: 8, autoSkip: true, maxRotation: 0 },
      },
      y: {
        type: 'linear' as const,
        grid: { color: c.grid },
        border: { display: false },
        ticks: {
          color: c.text,
          font: { size: 10 },
          callback: (value: number | string) => (amount ? formatMoney(Number(value), props.currency) : `${(Number(value) * 100).toFixed(0)}%`),
        },
      },
    },
  }
})

const ariaLabel = computed(() => {
  const last = props.series[props.series.length - 1]
  if (!last) return t('admin.cacheCharts.trend.empty')
  return t('admin.cacheCharts.trend.aria', {
    from: label(props.series[0].bucket_start),
    to: label(last.bucket_start),
    latest: formatInterval(last.token_hit_lower, last.token_hit_upper),
    events: markers.value.length,
  })
})
</script>
