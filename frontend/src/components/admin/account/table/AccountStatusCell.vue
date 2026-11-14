<template>
  <div class="account-status" data-test="account-status" :data-state="view.state" v-tip="tip">
    <StatusDot :tone="view.tone" />
    <component
      :is="view.state === 'temp' ? 'button' : 'span'"
      :type="view.state === 'temp' ? 'button' : undefined"
      class="account-status__label"
      :class="{
        'status-text-normal': view.state === 'ok',
        'account-status__label--error': view.state === 'error',
        'account-status__label--button': view.state === 'temp'
      }"
      :aria-label="view.state === 'temp' ? t('admin.accounts.status.viewTempUnschedDetails') : undefined"
      @click="view.state === 'temp' && emit('show-temp-unsched', account)"
    >
      {{ view.label }}<span v-if="view.code" class="account-status__code">{{ view.code }}</span>
    </component>
    <span v-if="detail" class="account-status__detail">{{ detail }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * Status column (prototype): a dot and a word. Normal reads in the secondary colour; paused (scheduling off) is the
 * shared hollow ring; rate limited, overloaded and temporarily unschedulable carry a small code and a countdown on
 * the page's shared clock; an error is red with its reason. Model-level limits are counted and listed in the tip.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { useLiveClock } from '@/composables/useLiveClock'
import { IQ_GATE_KEY_PREFIX } from '@/components/admin/account/iqCheckPresentation'
import type { Account } from '@/types'
import { vTip } from '@/utils/tip'
import { formatRemaining, formatShortTime, msLeft } from './accountTime'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ 'show-temp-unsched': [account: Account] }>()
const { t } = useI18n()
const now = useLiveClock()

type State = 'error' | 'inactive' | 'limited' | 'overloaded' | 'temp' | 'quota' | 'paused' | 'ok'
interface View { state: State; tone: StatusDotTone; label: string; code?: string; until?: string | null }

const exceeded = (used?: number | null, limit?: number | null) => typeof limit === 'number' && limit > 0 && typeof used === 'number' && used >= limit

// Countdowns and the state they end read the page's shared clock (one tick a second for the whole page).
const view = computed<View>(() => {
  const a = props.account
  const at = now.value
  if (a.status === 'error') return { state: 'error', tone: 'error', label: t('admin.accounts.status.error') }
  if (a.status !== 'active') return { state: 'inactive', tone: 'off', label: t('admin.accounts.status.inactive') }
  if (msLeft(a.rate_limit_reset_at, at) !== null) return { state: 'limited', tone: 'back', label: t('admin.accounts.status.rateLimited'), code: '429', until: a.rate_limit_reset_at }
  if (msLeft(a.overload_until, at) !== null) return { state: 'overloaded', tone: 'back', label: t('admin.accounts.status.overloaded'), code: '529', until: a.overload_until }
  if (msLeft(a.temp_unschedulable_until, at) !== null) return { state: 'temp', tone: 'back', label: t('admin.accounts.status.tempUnschedulable'), until: a.temp_unschedulable_until }
  if (exceeded(a.quota_used, a.quota_limit) || exceeded(a.quota_daily_used, a.quota_daily_limit) || exceeded(a.quota_weekly_used, a.quota_weekly_limit)) {
    return { state: 'quota', tone: 'back', label: t('admin.accounts.status.quotaExceeded') }
  }
  if (!a.schedulable) return { state: 'paused', tone: 'off', label: t('admin.accounts.status.paused') }
  return { state: 'ok', tone: 'ok', label: t('admin.accounts.status.active') }
})

type ModelLimit = { model: string; reset_at: string }
const modelLimits = computed<ModelLimit[]>(() => {
  const limits = (props.account.extra as Record<string, unknown> | undefined)?.model_rate_limits as Record<string, { rate_limit_reset_at: string }> | undefined
  const at = now.value
  return Object.entries(limits ?? {})
    .filter(([model, info]) => !model.startsWith(IQ_GATE_KEY_PREFIX) && msLeft(info.rate_limit_reset_at, at) !== null)
    .map(([model, info]) => ({ model, reset_at: info.rate_limit_reset_at }))
})

const detail = computed(() => {
  const state = view.value.state
  if (state === 'error') return props.account.error_message?.trim() ?? ''
  if (view.value.until) {
    const left = msLeft(view.value.until, now.value)
    return left === null ? '' : t('admin.accounts.table.statusUntil', { time: formatRemaining(t, left) })
  }
  if (modelLimits.value.length) return t('admin.accounts.table.modelLimits', { n: modelLimits.value.length })
  return ''
})

const tip = () => {
  const lines: string[] = []
  if (view.value.state === 'error' && props.account.error_message) lines.push(props.account.error_message)
  if (view.value.state === 'temp' && props.account.temp_unschedulable_reason) lines.push(props.account.temp_unschedulable_reason)
  if (view.value.until) lines.push(t('admin.accounts.status.tempUnschedulableUntil', { time: formatShortTime(view.value.until) }))
  for (const limit of modelLimits.value) lines.push(t('admin.accounts.status.modelRateLimitedUntil', { model: limit.model, time: formatShortTime(limit.reset_at) }))
  return lines.join('\n')
}
</script>

<style scoped>
.account-status {
  display: grid;
  grid-template-columns: 12px auto;
  row-gap: 2px;
  align-items: center;
  justify-content: start;
  min-width: 0;
}
.account-status__label {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  white-space: nowrap;
}
.account-status__label--error {
  color: var(--color-status-error-text);
  font-weight: var(--font-weight-medium);
}
.account-status__label--button {
  cursor: pointer;
  text-decoration: underline dotted 1px;
  text-underline-offset: 3px;
}
.account-status__code {
  display: inline-block;
  padding: 0 4px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-pill);
  color: var(--color-text-secondary);
  font-size: var(--font-size-micro);
  font-weight: var(--font-weight-medium);
  line-height: var(--line-height-micro);
}
.account-status__detail {
  grid-column: 2;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
