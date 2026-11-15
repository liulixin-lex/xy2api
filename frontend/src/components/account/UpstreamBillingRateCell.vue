<template>
  <div v-if="eligible" class="ratio" :class="{ 'ratio--stale': dimmed, 'ratio--detailed': detailed }">
    <!-- Information only, no control in it: the app's plain tip (7c, 7i-6). -->
    <span class="ratio__v" v-tip="rateTip" data-testid="upstream-billing-rate">{{ primaryValue }}</span>
    <button
      type="button"
      class="ratio__btn"
      :class="{ 'ratio__btn--busy': probing }"
      :disabled="probing"
      :aria-label="t('admin.accounts.upstreamBilling.manualProbe')"
      v-tip="t('admin.accounts.table.ratioProbe')"
      data-testid="upstream-billing-probe"
      @click="$emit('probe')"
    >
      <Icon name="refresh" size="xs" :class="{ 'animate-spin': probing }" />
    </button>
    <!-- The narrow card's details: what the tip says about a stale or failed probe, in the open (no hover on touch). -->
    <div v-if="detailed && (stale || failureText)" class="ratio__note" data-testid="upstream-billing-note">
      <span v-if="stale && Number.isFinite(receivedAt)">{{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: probeTime(snapshot?.received_at) }) }}</span>
      <span v-if="failureText">{{ failureText }}</span>
    </div>
  </div>
  <span v-else class="ratio__none">—</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { formatDate } from '@/utils/format'
import { vTip } from '@/utils/tip'
import { probeFailureText, upstreamRateView } from './upstreamBillingRate'
import type { Account, UpstreamBillingProbeSnapshot } from '@/types'

const props = withDefaults(defineProps<{
  account: Account
  now: number
  probing?: boolean
  globalProbeEnabled?: boolean
  /** The narrow card's details: the refresh button always shows, a stale or failed probe says so below the value. */
  detailed?: boolean
}>(), {
  globalProbeEnabled: true
})

defineEmits<{
  (event: 'probe'): void
}>()

const { t } = useI18n()
// The value, stale state and failure come from upstreamBillingRate.ts (the quality panel shows the same).
const view = computed(() => upstreamRateView(props.account, props.now))
const eligible = computed(() => view.value.eligible)
const snapshot = computed<UpstreamBillingProbeSnapshot | undefined>(() => view.value.snapshot)
const data = computed(() => view.value.data)
const probeEnabled = computed(() => view.value.probeEnabled)
const nextProbeAt = computed(() => view.value.nextProbeAt)
const receivedAt = computed(() => view.value.receivedAt)
const stale = computed(() => view.value.stale)
const currentEffectiveRate = computed(() => view.value.currentEffectiveRate)
const lastDetectedRate = computed(() => view.value.lastDetectedRate)
const elapsedSinceLastSuccess = computed(() => {
  if (!Number.isFinite(receivedAt.value)) return '-'
  const elapsedMinutes = Math.max(0, Math.floor((props.now - receivedAt.value) / 60_000))
  if (elapsedMinutes < 1) return t('admin.accounts.upstreamBilling.justNow')
  if (elapsedMinutes < 60) return t('admin.accounts.upstreamBilling.minutesAgo', { count: elapsedMinutes })
  const elapsedHours = Math.floor(elapsedMinutes / 60)
  if (elapsedHours < 24) return t('admin.accounts.upstreamBilling.hoursAgo', { count: elapsedHours })
  return t('admin.accounts.upstreamBilling.daysAgo', { count: Math.floor(elapsedHours / 24) })
})
const statusLabel = computed(() => {
  if (!snapshot.value) return t('admin.accounts.upstreamBilling.notProbed')
  if (snapshot.value.status === 'unsupported') return t('admin.accounts.upstreamBilling.unsupported')
  if (stale.value) return t('admin.accounts.upstreamBilling.stale')
  if (snapshot.value.status === 'failed') return t('admin.accounts.upstreamBilling.failed')
  return ''
})
const hasEffectiveRate = computed(() => view.value.hasEffectiveRate)
const primaryValue = computed(() => {
  if (view.value.shownValue) return view.value.shownValue
  if (snapshot.value?.status === 'unsupported') return statusLabel.value
  return '—'
})
const dimmed = computed(() => view.value.dimmed)
const failureText = computed(() => probeFailureText(view.value.failure, t))
/** Month, day and 24-hour time in the interface language (never "05:51 PM" in Chinese). */
const probeTime = (value?: string) =>
  (value && formatDate(value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })) || '-'

/** The hover tip: the rates in force, or the last probe's; why it failed; when it runs next; the probe switches. */
function rateTip(): string {
  const key = (name: string) => `admin.accounts.upstreamBilling.${name}`
  const lines: string[] = []
  const rates = data.value
  if (hasEffectiveRate.value && rates) {
    lines.push(t(key('groupRate'), { value: rates.group_rate_multiplier }))
    if (rates.user_rate_multiplier != null) lines.push(t(key('userRate'), { value: rates.user_rate_multiplier }))
    lines.push(rates.peak_rate_enabled
      ? t(key('peakRate'), { start: rates.peak_start, end: rates.peak_end, value: rates.peak_rate_multiplier, timezone: rates.timezone })
      : t(key('noPeakRate')))
    lines.push(t(key('effectiveRate'), { value: currentEffectiveRate.value ?? '-' }))
    lines.push(t(key('updatedAt'), { value: probeTime(snapshot.value?.received_at) }))
  } else if (stale.value && lastDetectedRate.value != null) {
    lines.push(t(key('lastDetectedRate'), { value: lastDetectedRate.value }))
    lines.push(t(key('lastDetectedAt'), { value: probeTime(snapshot.value?.received_at) }))
    lines.push(t(key('elapsedSince'), { value: elapsedSinceLastSuccess.value }))
  } else {
    lines.push(statusLabel.value || '-')
  }
  if (failureText.value) lines.push(failureText.value)
  if (probeEnabled.value && props.globalProbeEnabled !== false && nextProbeAt.value) lines.push(t(key('nextProbeAt'), { value: probeTime(nextProbeAt.value) }))
  lines.push(t(key('accountProbeState')) + t(key(probeEnabled.value ? 'enabled' : 'disabled')))
  if (props.globalProbeEnabled === false) lines.push(t(key('globalProbeState')) + t(key('disabled')))
  return lines.join('\n')
}
</script>

<style scoped>
/* "9.9999x", the 3px gap and the 18px button fit the column's 74px; a longer value ends in "…", the tip has the rate
   in force whole (12.5). */
.ratio {
  display: flex;
  align-items: center;
  gap: 3px;
  min-width: 0;
}
.ratio__v {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  font-variant-numeric: tabular-nums;
  line-height: var(--line-height-body);
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: help;
}
.ratio--stale .ratio__v,
.ratio__none {
  color: var(--color-text-muted);
}
.ratio__btn {
  display: inline-grid;
  width: 18px;
  height: 18px;
  flex: none;
  place-items: center;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  opacity: 0;
  transition: opacity var(--motion-fast) var(--ease-out), background-color var(--motion-fast) var(--ease-out);
}
.ratio__btn:focus-visible,
.ratio__btn--busy {
  opacity: 1;
}
.ratio__btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
/* The refresh icon shows while the cell is hovered (on touch screens it always shows). */
@media (hover: hover) {
  td:hover .ratio__btn {
    opacity: 1;
  }
  .ratio__btn:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
@media (hover: none) {
  .ratio__btn {
    opacity: 1;
  }
}
.ratio--detailed {
  flex-wrap: wrap;
}
.ratio--detailed .ratio__btn {
  opacity: 1;
}
.ratio__note {
  display: grid;
  flex-basis: 100%;
  gap: 2px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
</style>
