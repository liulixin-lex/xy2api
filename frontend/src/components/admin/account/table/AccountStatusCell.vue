<template>
  <div class="account-status" data-test="account-status" :data-state="view.state" v-tip="tip">
    <StatusDot :tone="view.tone" />
    <component
      :is="view.state === 'temp' ? 'button' : 'span'"
      :type="view.state === 'temp' ? 'button' : undefined"
      class="account-status__label"
      :class="{
        'status-text-normal': view.state === 'ok' || view.state === 'paused',
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
 * Status column (prototype): a dot and a word. Normal and paused (scheduling off, the shared hollow ring) read in the
 * shared secondary status text; rate limited, overloaded and temporarily unschedulable carry a small code and a
 * countdown on the page's shared clock; an error is red with its reason. Model-level limits are counted and listed in
 * the tip. Only a row with an end time still ahead reads the clock, so the others do not redraw every second (7d-5,
 * 7g-1: the backend keeps an end time after it passes).
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { currentTime, useLiveClock } from '@/composables/useLiveClock'
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

/** Time left until `until` on the page's shared clock; the clock is read only for an end time still ahead. */
const left = (until: string | null | undefined) => (msLeft(until, currentTime()) === null ? null : msLeft(until, now.value))

const view = computed<View>(() => {
  const a = props.account
  if (a.status === 'error') return { state: 'error', tone: 'error', label: t('admin.accounts.status.error') }
  if (a.status !== 'active') return { state: 'inactive', tone: 'off', label: t('admin.accounts.status.inactive') }
  if (left(a.rate_limit_reset_at) !== null) return { state: 'limited', tone: 'back', label: t('admin.accounts.status.rateLimited'), code: '429', until: a.rate_limit_reset_at }
  if (left(a.overload_until) !== null) return { state: 'overloaded', tone: 'back', label: t('admin.accounts.status.overloaded'), code: '529', until: a.overload_until }
  if (left(a.temp_unschedulable_until) !== null) return { state: 'temp', tone: 'back', label: t('admin.accounts.status.tempUnschedulable'), until: a.temp_unschedulable_until }
  if (exceeded(a.quota_used, a.quota_limit) || exceeded(a.quota_daily_used, a.quota_daily_limit) || exceeded(a.quota_weekly_used, a.quota_weekly_limit)) {
    return { state: 'quota', tone: 'back', label: t('admin.accounts.status.quotaExceeded') }
  }
  if (!a.schedulable) return { state: 'paused', tone: 'off', label: t('admin.accounts.status.paused') }
  return { state: 'ok', tone: 'ok', label: t('admin.accounts.status.active') }
})

type ModelLimit = { model: string; reset_at: string }
const modelLimits = computed<ModelLimit[]>(() => {
  const limits = (props.account.extra as Record<string, unknown> | undefined)?.model_rate_limits as Record<string, { rate_limit_reset_at: string }> | undefined
  return Object.entries(limits ?? {})
    .filter(([model, info]) => !model.startsWith(IQ_GATE_KEY_PREFIX) && left(info?.rate_limit_reset_at) !== null)
    .map(([model, info]) => ({ model, reset_at: info.rate_limit_reset_at }))
})

const detail = computed(() => {
  const state = view.value.state
  if (state === 'error') return props.account.error_message?.trim() ?? ''
  if (view.value.until) {
    const ms = left(view.value.until)
    return ms === null ? '' : t('admin.accounts.table.statusUntil', { time: formatRemaining(t, ms) })
  }
  if (modelLimits.value.length) return t('admin.accounts.table.modelLimits', { n: modelLimits.value.length })
  return ''
})

const tip = () => {
  const lines: string[] = []
  if (view.value.state === 'error' && props.account.error_message) lines.push(props.account.error_message)
  if (view.value.state === 'temp' && props.account.temp_unschedulable_reason) lines.push(props.account.temp_unschedulable_reason)
  // The countdown line may be cut at narrow widths: the tip has it whole, with the end time.
  if (view.value.until) {
    if (detail.value) lines.push(detail.value)
    lines.push(t('admin.accounts.status.tempUnschedulableUntil', { time: formatShortTime(view.value.until) }))
  }
  for (const limit of modelLimits.value) lines.push(t('admin.accounts.status.modelRateLimitedUntil', { model: limit.model, time: formatShortTime(limit.reset_at) }))
  return lines.join('\n')
}
</script>

<style scoped>
/* The words take the cell's primary colour; .status-text-normal (shared) sets normal and paused apart (7e-4). */
.account-status {
  color: var(--color-text-primary);
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
