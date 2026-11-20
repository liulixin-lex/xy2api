<template>
  <div class="space-y-1.5" :data-variant="variant">
    <div>
      <div v-if="variant === 'tooltip'" class="mb-1 text-xs font-semibold text-gray-300">{{ t('usage.tokenDetails') }}</div>
      <div v-if="row.input_tokens > 0 && !hasImageInputTokens(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.inputTokens') }}</span>
        <span :class="tone.value">{{ row.input_tokens.toLocaleString() }}</span>
      </div>
      <div v-if="hasImageInputTokens(row) && textInputTokens(row) > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.inputTokens') }}</span>
        <span :class="tone.value">{{ textInputTokens(row).toLocaleString() }}</span>
      </div>
      <div v-if="hasImageInputTokens(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('usage.imageInputTokens') }}</span>
        <span :class="tone.fuchsia">{{ row.image_input_tokens.toLocaleString() }}</span>
      </div>
      <div v-if="row.output_tokens > 0 && !hasImageOutputTokens(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.outputTokens') }}</span>
        <span :class="tone.value">{{ row.output_tokens.toLocaleString() }}</span>
      </div>
      <div v-if="hasImageOutputTokens(row) && textOutputTokens(row) > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.outputTokens') }}</span>
        <span :class="tone.value">{{ textOutputTokens(row).toLocaleString() }}</span>
      </div>
      <div v-if="hasImageOutputTokens(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('usage.imageOutputTokens') }}</span>
        <span :class="tone.pink">{{ row.image_output_tokens.toLocaleString() }}</span>
      </div>
      <div v-if="row.cache_creation_tokens > 0">
        <!-- 有 5m/1h 明细时，展开显示 -->
        <template v-if="row.cache_creation_5m_tokens > 0 || row.cache_creation_1h_tokens > 0">
          <div v-if="row.cache_creation_5m_tokens > 0" class="flex items-center justify-between gap-4">
            <span class="flex items-center gap-1.5" :class="tone.label">
              {{ t('admin.usage.cacheCreation5mTokens') }}
              <span class="inline-flex items-center rounded px-1 py-px text-xs font-medium leading-tight ring-1 ring-inset" :class="tone.badge5m">5m</span>
            </span>
            <span :class="tone.value">{{ row.cache_creation_5m_tokens.toLocaleString() }}</span>
          </div>
          <div v-if="row.cache_creation_1h_tokens > 0" class="flex items-center justify-between gap-4">
            <span class="flex items-center gap-1.5" :class="tone.label">
              {{ t('admin.usage.cacheCreation1hTokens') }}
              <span class="inline-flex items-center rounded px-1 py-px text-xs font-medium leading-tight ring-1 ring-inset" :class="tone.badge1h">1h</span>
            </span>
            <span :class="tone.value">{{ row.cache_creation_1h_tokens.toLocaleString() }}</span>
          </div>
        </template>
        <!-- 无明细时，只显示聚合值 -->
        <div v-else class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('admin.usage.cacheCreationTokens') }}</span>
          <span :class="tone.value">{{ row.cache_creation_tokens.toLocaleString() }}</span>
        </div>
      </div>
      <p
        v-if="row.anthropic_cache_ttl_policy != null && row.cache_creation_tokens > (row.cache_creation_5m_tokens || 0) + (row.cache_creation_1h_tokens || 0)"
        :class="tone.note"
      >{{ t((row.cache_creation_5m_tokens || 0) + (row.cache_creation_1h_tokens || 0) > 0 ? 'usage.anthropicCacheTTL.partialDetail' : 'usage.anthropicCacheTTL.unknownDetail') }}</p>
      <div v-if="row.cache_ttl_overridden" class="flex items-center justify-between gap-4">
        <span class="flex items-center gap-1.5" :class="tone.label">
          {{ t('usage.cacheTtlOverriddenLabel') }}
          <span class="inline-flex items-center rounded px-1 py-px text-xs font-medium leading-tight ring-1 ring-inset" :class="tone.badgeOverride">{{ t('usage.cacheTtlOverriddenLabel') }}</span>
        </span>
        <span :class="tone.rose">{{ row.cache_creation_1h_tokens > 0 ? t('usage.cacheTtlOverridden1h') : t('usage.cacheTtlOverridden5m') }}</span>
      </div>
      <div v-if="row.cache_read_tokens > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.cacheReadTokens') }}</span>
        <span :class="tone.value">{{ row.cache_read_tokens.toLocaleString() }}</span>
      </div>
      <!-- 缓存读取占比按系统设置显示；媒体请求没有这项 -->
      <div v-if="showCacheHitRate && hasUsageTokenMetrics(row)" class="flex items-center justify-between gap-4" :title="t('usage.cacheHitRateHint')">
        <span :class="tone.label">{{ t('usage.cacheHitRate') }}</span>
        <span data-testid="usage-cache-hit-rate" :class="tone.value">{{ formatCacheHitRate(row) }}</span>
      </div>
    </div>
    <div class="flex items-center justify-between gap-6 border-t pt-1.5" :class="tone.divider">
      <span :class="tone.label">{{ t('usage.totalTokens') }}</span>
      <span :class="tone.total">{{ ((row.input_tokens || 0) + (row.output_tokens || 0) + (row.cache_creation_tokens || 0) + (row.cache_read_tokens || 0)).toLocaleString() }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { hasImageInputTokens, hasImageOutputTokens, textInputTokens, textOutputTokens } from '@/utils/imageUsage'
import { formatCacheHitRate, hasUsageTokenMetrics } from '@/utils/usageMetrics'
import type { AdminUsageLog } from '@/types'

/**
 * A usage row's token breakdown, the same lines and rules for the table's hover tip (dark tooltip) and the request
 * detail dialog (themed panel).
 */
const props = withDefaults(defineProps<{
  row: AdminUsageLog
  variant?: 'tooltip' | 'panel'
  showCacheHitRate?: boolean
}>(), {
  variant: 'tooltip',
  showCacheHitRate: false,
})

const { t } = useI18n()

const TONES = {
  tooltip: {
    label: 'text-gray-400',
    value: 'font-medium tabular-nums text-white',
    fuchsia: 'font-medium tabular-nums text-fuchsia-300',
    pink: 'font-medium tabular-nums text-pink-300',
    rose: 'font-medium text-rose-400',
    total: 'font-semibold tabular-nums text-blue-400',
    note: 'text-gray-300',
    divider: 'border-gray-700',
    badge5m: 'bg-amber-500/20 text-amber-400 ring-amber-500/30',
    badge1h: 'bg-orange-500/20 text-orange-400 ring-orange-500/30',
    badgeOverride: 'bg-rose-500/20 text-rose-400 ring-rose-500/30',
  },
  panel: {
    label: 'text-gray-500 dark:text-gray-400',
    value: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    fuchsia: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    pink: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    rose: 'font-medium text-rose-600 dark:text-rose-400',
    total: 'font-semibold tabular-nums text-gray-900 dark:text-white',
    note: 'text-gray-500 dark:text-gray-400',
    divider: 'border-gray-200 dark:border-dark-600',
    badge5m: 'bg-amber-50 text-amber-700 ring-amber-200 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-500/30',
    badge1h: 'bg-orange-50 text-orange-700 ring-orange-200 dark:bg-orange-500/10 dark:text-orange-300 dark:ring-orange-500/30',
    badgeOverride: 'bg-rose-50 text-rose-700 ring-rose-200 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-500/30',
  },
} as const

const tone = computed(() => TONES[props.variant])
</script>
