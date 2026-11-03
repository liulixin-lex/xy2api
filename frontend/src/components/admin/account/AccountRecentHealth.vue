<template>
  <!-- “近期表现”列：依次是失败、首字、缓存保持，各自带文字标签；没有可显示的项时显示短横线 -->
  <div v-if="failureLine || firstOutputLine || cacheLine" class="flex flex-col items-start gap-1 whitespace-nowrap">
    <a
      v-if="failureLine"
      :href="errorsHref"
      target="_blank"
      rel="noopener"
      data-test="recent-failures"
      class="text-[11px] font-medium leading-4 tabular-nums hover:underline"
      :class="failureLine.tone"
      :title="failureLine.title"
    >{{ failureLine.text }}</a>
    <span
      v-if="firstOutputLine"
      data-test="recent-first-output"
      class="text-[11px] leading-4 tabular-nums"
      :class="firstOutputLine.tone"
      :title="firstOutputLine.title"
    >{{ firstOutputLine.text }}</span>
    <span
      v-if="cacheLine"
      data-test="recent-cache"
      class="text-[11px] leading-4 tabular-nums"
      :class="cacheLine.tone"
      :title="cacheLine.title"
    >{{ cacheLine.text }}</span>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500" data-test="recent-empty" :title="emptyTitle">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountRecentHealth, AccountRecentHealthLoadState } from '@/api/admin/accounts'
import { LATENCY_TEXT_CLASSES, firstTokenSeverity } from '@/utils/latencyHealth'

const props = withDefaults(
  defineProps<{
    accountId: number
    health?: AccountRecentHealth | null
    platform?: string
    loadState?: AccountRecentHealthLoadState
  }>(),
  { loadState: 'loaded' }
)

const { t } = useI18n()

// 与接口的 min_display_samples / min_color_samples 相同：样本少于 20 个只显示计数，满 50 个才着色。
const MIN_DISPLAY_SAMPLES = 20
const MIN_COLOR_SAMPLES = 50
const NEUTRAL = 'text-gray-500 dark:text-gray-400'
const WARN = 'text-amber-600 dark:text-amber-400'
const DANGER = 'text-red-600 dark:text-red-400'

const failure = computed(() => props.health?.failure ?? null)
const firstOutput = computed(() => props.health?.first_output ?? null)
const cache = computed(() => props.health?.cache ?? null)

const pct = (ratio: number, digits = 0) => `${(ratio * 100).toFixed(digits)}%`
const share = (part: number, total: number) => (total > 0 ? pct(part / total) : '-')
// 着色看的值（区间下限、P90）向下截断显示，阈值附近显示值与颜色不矛盾；区间上限向上取，显示的区间不比实际窄。
const floorPct = (ratio: number) => `${(Math.floor(ratio * 1000 + 1e-9) / 10).toFixed(1)}%`
const ceilPct = (ratio: number) => `${(Math.ceil(ratio * 1000 - 1e-9) / 10).toFixed(1)}%`
const seconds = (ms: number) => `${(Math.floor(ms / 100) / 10).toFixed(1)}s`
const span = (spanSeconds: number) =>
  spanSeconds < 60
    ? t('admin.accounts.recentSpanUnderMinute')
    : t('admin.accounts.recentSpanMinutes', { n: Math.round(spanSeconds / 60) })

// 失败为 0 时不显示；满 20 次显示比例，按 Wilson 下限着色，否则灰色计数。
const failureLine = computed(() => {
  const f = failure.value
  if (!f || f.failures <= 0) return null
  const counts = { failures: f.failures, calls: f.calls }
  const enough = f.calls >= MIN_DISPLAY_SAMPLES
  let tone = NEUTRAL
  if (enough && f.wilson_low >= 0.1) tone = DANGER
  else if (enough && f.wilson_low >= 0.05) tone = WARN
  return {
    text: enough
      ? t('admin.accounts.recentFailureRate', { ...counts, rate: pct(f.failures / f.calls, 1) })
      : t('admin.accounts.recentFailureCount', counts),
    tone,
    title: [
      t('admin.accounts.recentFailureTitleInterval', {
        low: floorPct(f.wilson_low),
        high: ceilPct(f.wilson_high),
        calls: f.calls,
        span: span(f.span_seconds)
      }),
      t('admin.accounts.recentFailureTitleKinds', {
        timeouts: f.first_output_timeouts,
        stream: f.stream_failures,
        other: f.other_failures
      }),
      t('admin.accounts.recentFailureTitleRule'),
      t('admin.accounts.recentFailureTitleNote')
    ].join('\n')
  }
})

const firstOutputLine = computed(() => {
  const s = firstOutput.value
  if (!s) return null
  const window = { count: s.samples, span: span(s.span_seconds) }
  if (s.samples < MIN_DISPLAY_SAMPLES) {
    return {
      text: t('admin.accounts.recentFirstOutputFew', { count: s.samples }),
      tone: NEUTRAL,
      title: t('admin.accounts.recentFirstOutputFewTitle', { ...window, min: MIN_DISPLAY_SAMPLES })
    }
  }
  return {
    text: t('admin.accounts.recentFirstOutputP90', { value: seconds(s.p90_ms) }),
    tone: s.samples >= MIN_COLOR_SAMPLES ? LATENCY_TEXT_CLASSES[firstTokenSeverity(s.p90_ms)] : NEUTRAL,
    title: [
      t('admin.accounts.recentFirstOutputTitleStats', { p50: seconds(s.p50_ms), slow: pct(s.slow_share) }),
      t('admin.accounts.recentFirstOutputTitleWindow', window),
      t('admin.accounts.recentFirstOutputTitleRule')
    ].join('\n')
  }
})

// 保持率 < 90% 黄、< 75% 红；Gemini 是隐式缓存，不着色。向下取整，避免黄色时显示成 90%。
const cacheLine = computed(() => {
  const c = cache.value
  if (!c) return null
  const window = { count: c.samples, span: span(c.span_seconds) }
  if (c.samples < MIN_DISPLAY_SAMPLES) {
    return {
      text: t('admin.accounts.recentCacheFew', { count: c.samples }),
      tone: NEUTRAL,
      title: t('admin.accounts.recentCacheFewTitle', { ...window, min: MIN_DISPLAY_SAMPLES })
    }
  }
  const kept = c.kept / c.samples
  const gemini = props.platform === 'gemini'
  let tone = NEUTRAL
  if (c.samples >= MIN_COLOR_SAMPLES && !gemini) {
    if (kept < 0.75) tone = DANGER
    else if (kept < 0.9) tone = WARN
  }
  const title = [
    t('admin.accounts.recentCacheTitleBuckets', {
      zero: share(c.zero, c.samples),
      partial: share(c.partial, c.samples),
      mid: share(c.mid, c.samples),
      full: share(c.full, c.samples)
    }),
    t('admin.accounts.recentCacheTitleTokens', { value: share(c.read_tokens, c.prompt_tokens) }),
    t('admin.accounts.recentCacheTitleWindow', window),
    t(gemini ? 'admin.accounts.recentCacheTitleGemini' : 'admin.accounts.recentCacheTitleRule')
  ]
  return {
    text: t('admin.accounts.recentCacheKept', { value: `${Math.floor((c.kept * 100) / c.samples)}%` }),
    tone,
    title: title.join('\n')
  }
})

// 只有数据已加载时，短横线才下“没有数据”的结论；加载中或加载失败时如实说明。
// 有请求但没有失败、也没有续轮样本时，说明这一点，而不是“没有可计入的请求”。
const emptyTitle = computed(() => {
  if (!props.health && props.loadState === 'loading') return t('admin.accounts.recentHealthLoadingTitle')
  if (!props.health && props.loadState === 'failed') return t('admin.accounts.recentHealthFailedTitle')
  return failure.value && failure.value.calls > 0
    ? t('admin.accounts.recentHealthNoFailuresTitle', { calls: failure.value.calls })
    : t('admin.accounts.recentHealthEmptyTitle')
})

// 现有的“使用记录 → 错误请求”页，UsageView 读取 tab 与 account_id 并筛选该账号。
// 与 createWebHistory(BASE_URL) 生成的地址一致，不依赖注入的 router。
const errorsHref = computed(() => {
  const query = new URLSearchParams({ tab: 'errors', account_id: String(props.accountId) })
  return `${import.meta.env.BASE_URL.replace(/\/+$/, '')}/admin/usage?${query}`
})
</script>
