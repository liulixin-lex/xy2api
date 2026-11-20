<template>
  <div class="space-y-1.5" :data-variant="variant">
    <!-- Cost Breakdown -->
    <div class="mb-2 border-b pb-1.5" :class="tone.divider">
      <div v-if="variant === 'tooltip'" class="mb-1 text-xs font-semibold text-gray-300">{{ t('usage.costDetails') }}</div>
      <div v-if="row.input_cost > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.inputCost') }}</span>
        <span :class="tone.value">${{ row.input_cost.toFixed(8) }}</span>
      </div>
      <div v-if="hasImageInputCost(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('usage.imageInputCost') }}</span>
        <span :class="tone.fuchsia">${{ row.image_input_cost.toFixed(8) }}</span>
      </div>
      <div v-if="row.output_cost > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.outputCost') }}</span>
        <span :class="tone.value">${{ row.output_cost.toFixed(8) }}</span>
      </div>
      <div v-if="hasImageOutputCost(row)" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('usage.imageOutputCost') }}</span>
        <span :class="tone.pink">${{ row.image_output_cost.toFixed(8) }}</span>
      </div>
      <!-- Token billing: show unit prices per 1M tokens -->
      <template v-if="!isImageUsage(row) && (!row.billing_mode || row.billing_mode === BILLING_MODE_TOKEN)">
        <div v-if="textInputTokens(row) > 0" class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.inputTokenPrice') }}</span>
          <span :class="tone.sky">{{ formatTokenPricePerMillion(row.input_cost, textInputTokens(row)) }} {{ t('usage.perMillionTokens') }}</span>
        </div>
        <div v-if="hasImageInputTokens(row)" class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageInputTokenPrice') }}</span>
          <span :class="tone.fuchsia">{{ formatTokenPricePerMillion(row.image_input_cost ?? 0, row.image_input_tokens) }} {{ t('usage.perMillionTokens') }}</span>
        </div>
        <div v-if="row.output_cost > 0 && textOutputTokens(row) > 0" class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.outputTokenPrice') }}</span>
          <span :class="tone.violet">{{ formatTokenPricePerMillion(row.output_cost, textOutputTokens(row)) }} {{ t('usage.perMillionTokens') }}</span>
        </div>
        <div v-if="hasImageOutputTokens(row)" class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageOutputTokenPrice') }}</span>
          <span :class="tone.pink">{{ formatTokenPricePerMillion(row.image_output_cost ?? 0, row.image_output_tokens) }} {{ t('usage.perMillionTokens') }}</span>
        </div>
      </template>
      <template v-else-if="isImageUsage(row)">
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageCount') }}</span>
          <span :class="tone.value">{{ row.image_count }}{{ t('usage.imageUnit') }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageBillingSize') }}</span>
          <span :class="tone.value">{{ formatImageBillingSize(row, t) }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageSizeSource') }}</span>
          <span :class="tone.value">{{ formatImageSizeSource(row, t) }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageInputSize') }}</span>
          <span :class="tone.value">{{ formatImageInputSize(row, t) }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageOutputSize') }}</span>
          <span :class="tone.value">{{ formatImageOutputSize(row, t) }}</span>
        </div>
        <div v-if="formatImageSizeBreakdown(row)" class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageSizeBreakdown') }}</span>
          <span :class="tone.value">{{ formatImageSizeBreakdown(row) }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageUnitPrice') }}</span>
          <span :class="tone.sky">${{ imageUnitPrice(row).toFixed(8) }}</span>
        </div>
        <div class="flex items-center justify-between gap-4">
          <span :class="tone.label">{{ t('usage.imageTotalPrice') }}</span>
          <span :class="tone.value">${{ row.total_cost?.toFixed(8) || '0.00000000' }}</span>
        </div>
      </template>
      <div v-else class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('usage.unitPrice') }}</span>
        <span :class="tone.sky">${{ row.total_cost?.toFixed(8) || '0.00000000' }}</span>
      </div>
      <div v-if="row.cache_creation_cost > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.cacheCreationCost') }}</span>
        <span :class="tone.value">${{ row.cache_creation_cost.toFixed(8) }}</span>
      </div>
      <div v-if="row.cache_read_cost > 0" class="flex items-center justify-between gap-4">
        <span :class="tone.label">{{ t('admin.usage.cacheReadCost') }}</span>
        <span :class="tone.value">${{ row.cache_read_cost.toFixed(8) }}</span>
      </div>
    </div>
    <!-- Rate and Summary -->
    <div class="flex items-center justify-between gap-6">
      <span :class="tone.label">{{ t('usage.serviceTier') }}</span>
      <span :class="tone.cyan">{{ getUsageServiceTierLabel(row.service_tier, t) }}</span>
    </div>
    <div class="flex items-center justify-between gap-6">
      <span :class="tone.label">{{ t('usage.rate') }}</span>
      <span :class="tone.blue">{{ formatMultiplier(row.rate_multiplier ?? 1) }}x</span>
    </div>
    <div class="flex items-center justify-between gap-6">
      <span :class="tone.label">{{ t('usage.original') }}</span>
      <span :class="tone.value">${{ row.total_cost?.toFixed(8) || '0.00000000' }}</span>
    </div>
    <div class="flex items-center justify-between gap-6">
      <span :class="tone.label">{{ t('usage.userBilled') }}</span>
      <span :class="tone.billed">${{ row.actual_cost?.toFixed(8) || '0.00000000' }}</span>
    </div>
    <!-- Account billing (separated from user billing) -->
    <template v-if="showAccountBilling">
      <div class="flex items-center justify-between gap-6 border-t pt-1.5" :class="tone.divider">
        <span :class="tone.label">{{ t('usage.accountMultiplier') }}</span>
        <span :class="tone.blue">{{ formatMultiplier(row.account_rate_multiplier ?? 1) }}x</span>
      </div>
      <div class="flex items-center justify-between gap-6">
        <span :class="tone.label">{{ t('usage.accountBilled') }}</span>
        <span :class="tone.billed">${{ accountBilled(row).toFixed(8) }}</span>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatMultiplier } from '@/utils/formatters'
import { formatTokenPricePerMillion } from '@/utils/usagePricing'
import { getUsageServiceTierLabel } from '@/utils/usageServiceTier'
import { BILLING_MODE_TOKEN, imageUnitPrice, isImageUsage } from '@/utils/billingMode'
import {
  formatImageBillingSize,
  formatImageInputSize,
  formatImageOutputSize,
  formatImageSizeBreakdown,
  formatImageSizeSource,
  hasImageInputCost,
  hasImageInputTokens,
  hasImageOutputCost,
  hasImageOutputTokens,
  textInputTokens,
  textOutputTokens,
} from '@/utils/imageUsage'
import { accountBilled } from './usageRow'
import type { AdminUsageLog } from '@/types'

/**
 * A usage row's cost breakdown, the same lines and rules for the table's hover tip (dark tooltip) and the request
 * detail dialog (themed panel). Account billing shows only where the page allows it (not on the user page).
 */
const props = withDefaults(defineProps<{
  row: AdminUsageLog
  variant?: 'tooltip' | 'panel'
  showAccountBilling?: boolean
}>(), {
  variant: 'tooltip',
  showAccountBilling: false,
})

const { t } = useI18n()

const TONES = {
  tooltip: {
    label: 'text-gray-400',
    value: 'font-medium tabular-nums text-white',
    fuchsia: 'font-medium tabular-nums text-fuchsia-300',
    pink: 'font-medium tabular-nums text-pink-300',
    sky: 'font-medium tabular-nums text-sky-300',
    violet: 'font-medium tabular-nums text-violet-300',
    cyan: 'font-semibold text-cyan-300',
    blue: 'font-semibold tabular-nums text-blue-400',
    billed: 'font-semibold tabular-nums text-green-400',
    divider: 'border-gray-700',
  },
  panel: {
    label: 'text-gray-500 dark:text-gray-400',
    value: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    fuchsia: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    pink: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    sky: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    violet: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    cyan: 'font-medium text-gray-900 dark:text-gray-100',
    blue: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    billed: 'font-semibold tabular-nums text-green-700 dark:text-green-400',
    divider: 'border-gray-200 dark:border-dark-600',
  },
} as const

const tone = computed(() => TONES[props.variant])
</script>
