<template>
  <div v-if="view.adjusted || view.insufficient" class="mt-1 flex flex-wrap justify-end gap-1" :data-testid="'share-factors-' + member.account_id">
    <HelpTooltip v-if="view.health" width-class="w-80">
      <template #trigger>
        <span class="cursor-help rounded bg-amber-50 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300" :data-testid="'share-factor-health-' + member.account_id">
          {{ t('admin.shareScheduling.traffic.factors.health', { value: formatShareFactor(view.health.factor) }) }}
        </span>
      </template>
      <div class="space-y-1" :data-testid="'share-factor-detail-' + member.account_id">
        <p class="font-medium">{{ breakdown }}</p>
        <p>{{ healthReason }}</p>
        <p v-if="rampReason">{{ rampReason }}</p>
      </div>
    </HelpTooltip>
    <HelpTooltip v-if="view.ramp" width-class="w-80">
      <template #trigger>
        <span class="cursor-help rounded bg-sky-50 px-1.5 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-900/20 dark:text-sky-300" :data-testid="'share-factor-ramp-' + member.account_id">
          {{ view.ramp.remainingMinutes === null
            ? t('admin.shareScheduling.traffic.factors.rampNoTime', { value: formatShareFactor(view.ramp.factor) })
            : t('admin.shareScheduling.traffic.factors.ramp', { value: formatShareFactor(view.ramp.factor), minutes: view.ramp.remainingMinutes }) }}
        </span>
      </template>
      <div class="space-y-1">
        <p class="font-medium">{{ breakdown }}</p>
        <p v-if="healthReason && view.health">{{ healthReason }}</p>
        <p>{{ rampReason }}</p>
      </div>
    </HelpTooltip>
    <HelpTooltip v-if="view.insufficient && !view.health" width-class="w-72">
      <template #trigger>
        <span class="cursor-help rounded bg-gray-100 px-1.5 py-0.5 text-[11px] text-gray-500 dark:bg-dark-700 dark:text-dark-300" :data-testid="'share-factor-insufficient-' + member.account_id">
          {{ t('admin.shareScheduling.traffic.factors.insufficient') }}
        </span>
      </template>
      <p>{{ t('admin.shareScheduling.traffic.factors.insufficientReason', { minutes: member.health?.window_minutes ?? 30 }) }}</p>
    </HelpTooltip>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { ShareMember } from '@/api/admin/shareScheduling'
import { formatShareFactor, formatShareWeight, shareFactors } from '@/utils/shareFactors'
import { formatSharePercent } from '@/utils/shareScheduling'

/**
 * Marks next to an account's expected share when an automatic factor
 * changes it, with the breakdown and the reason on hover.
 */
const props = defineProps<{ member: ShareMember; now?: number }>()

const { t, te } = useI18n()
const view = computed(() => shareFactors(props.member, props.now))

const breakdown = computed(() => t('admin.shareScheduling.traffic.factors.breakdown', {
  loadFactor: props.member.effective_load_factor,
  health: formatShareFactor(props.member.health_factor),
  ramp: formatShareFactor(props.member.ramp_factor),
  weight: formatShareWeight(props.member.effective_weight),
  share: formatSharePercent(props.member.expected_share),
  base: formatSharePercent(props.member.base_share)
}))

const healthReason = computed(() => {
  const health = view.value.health
  if (!health) return ''
  return t('admin.shareScheduling.traffic.factors.healthReason', {
    minutes: health.windowMinutes,
    rate: formatSharePercent(health.rate),
    baseline: formatSharePercent(health.baseline)
  })
})

const rampReason = computed(() => {
  const ramp = view.value.ramp
  if (!ramp) return ''
  const causeKey = `admin.shareScheduling.traffic.factors.rampReasons.${ramp.cause}`
  const cause = ramp.cause && ramp.sinceMinutes !== null && (typeof te !== 'function' || te(causeKey))
    ? t(causeKey, { since: ramp.sinceMinutes })
    : ''
  const remaining = ramp.remainingMinutes === null ? '' : t('admin.shareScheduling.traffic.factors.rampRemaining', { minutes: ramp.remainingMinutes })
  return `${cause}${remaining}`
})
</script>
