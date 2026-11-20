<template>
  <BaseDialog :show="true" :title="t('usage.tierDetails.title')" @close="$emit('close')">
    <p v-if="!row.service_tier_details" class="mb-3 text-sm text-gray-500 dark:text-gray-400">{{ t('usage.tierDetails.noEvidence') }}</p>
    <dl class="divide-y divide-gray-100 text-sm dark:divide-dark-600">
      <div v-for="[key, value] in fields" :key="key" class="grid grid-cols-2 items-baseline gap-3 py-2">
        <dt class="text-gray-600 dark:text-gray-400">{{ t('usage.tierDetails.' + key) }}</dt>
        <dd class="break-words text-right font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ value }}</dd>
      </div>
    </dl>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UsageLog } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { formatUsd } from '@/utils/format'
import { getUsageServiceTierLabel } from '@/utils/usageServiceTier'

const props = defineProps<{ row: UsageLog; admin?: boolean }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const namedSources = new Set(['group', 'channel', 'model_default', 'priority_unit_prices', 'fallback', 'litellm',
  'builtin', 'outbound_quote', 'disabled_response_tier'])
const sourceLabel = (value?: string) => value && namedSources.has(value) ? t('usage.tierDetails.values.' + value) : value
const fields = computed(() => {
  const d = props.row.service_tier_details
  const tier = (value?: string | null) => getUsageServiceTierLabel(value, t)
  const rows: [string, string][] = []
  const add = (key: string, value?: number | string | null) => {
    if (value != null && value !== '') rows.push([key, String(value)])
  }
  if (d) {
    let requested = tier(d.client_tier)
    if (d.client_presence === 'missing') requested = t('usage.tierDetails.missing')
    if (d.client_presence === 'null') requested = t('usage.tierDetails.explicitNull')
    add('client_tier', requested)
    add('outbound_tier', tier(d.outbound_tier))
    add('observed_tier', tier(d.observed_tier))
  }
  add('settled_tier', tier(d?.settled_tier ?? props.row.service_tier))
  if (d) {
    add('billing_model', d.billing_model)
    add('price_source', sourceLabel(d.price_source))
    add('multiplier_source', sourceLabel(d.multiplier_source))
    if (d.tier_multiplier != null) add('tier_multiplier', String(d.tier_multiplier) + '×')
    if (d.free_tier) add('free_tier', tier(d.free_tier))
    for (const key of ['standard_actual_cost', 'outbound_actual_cost', 'actual_cost'] as const) {
      if (d[key] != null) add(key, formatUsd(d[key], 8))
    }
    add('cap_reason', sourceLabel(d.cap_reason))
    if (props.admin) {
      add('price_revision', d.price_revision)
      if (d.response_tier_evidence?.length) add('response_tier_evidence', d.response_tier_evidence.join(', '))
      if (d.response_tier_conflict) add('response_tier_conflict', t('usage.tierDetails.conflict'))
      for (const key of ['source', 'reason', 'policy_version', 'origin_group_id', 'serving_group_id'] as const) {
        add(key, d[key])
      }
    }
  }
  return rows
})
</script>
