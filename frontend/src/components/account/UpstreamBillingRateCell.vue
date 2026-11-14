<template>
  <div v-if="eligible" class="ratio" :class="{ 'ratio--stale': dimmed, 'ratio--detailed': detailed }">
    <HelpTooltip width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="upstream-billing-details">
      <template #trigger>
        <span class="ratio__v" data-testid="upstream-billing-rate">{{ primaryValue }}</span>
      </template>
      <div class="space-y-1">
        <template v-if="hasEffectiveRate && data">
          <p>{{ t('admin.accounts.upstreamBilling.groupRate', { value: data.group_rate_multiplier }) }}</p>
          <p v-if="data.user_rate_multiplier != null">
            {{ t('admin.accounts.upstreamBilling.userRate', { value: data.user_rate_multiplier }) }}
          </p>
          <p>
            {{
              data.peak_rate_enabled
                ? t('admin.accounts.upstreamBilling.peakRate', {
                    start: data.peak_start,
                    end: data.peak_end,
                    value: data.peak_rate_multiplier,
                    timezone: data.timezone
                  })
                : t('admin.accounts.upstreamBilling.noPeakRate')
            }}
          </p>
          <p>{{ t('admin.accounts.upstreamBilling.effectiveRate', { value: currentEffectiveRate ?? '-' }) }}</p>
          <p>{{ t('admin.accounts.upstreamBilling.updatedAt', { value: formatDate(snapshot?.received_at) }) }}</p>
        </template>
        <template v-else-if="stale && lastDetectedRate != null">
          <p data-testid="upstream-billing-last-rate">
            {{ t('admin.accounts.upstreamBilling.lastDetectedRate', { value: lastDetectedRate }) }}
          </p>
          <p data-testid="upstream-billing-last-time">
            {{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: formatDate(snapshot?.received_at) }) }}
          </p>
          <p data-testid="upstream-billing-elapsed">
            {{ t('admin.accounts.upstreamBilling.elapsedSince', { value: elapsedSinceLastSuccess }) }}
          </p>
        </template>
        <p v-else>{{ statusLabel || '-' }}</p>
        <p v-if="failureReason" data-testid="upstream-billing-failure">
          {{ t('admin.accounts.table.ratioFailure', { reason: failureReason }) }}
        </p>
        <p
          v-if="probeEnabled && globalProbeEnabled !== false && nextProbeAt"
          data-testid="upstream-billing-next-probe"
        >
          {{ t('admin.accounts.upstreamBilling.nextProbeAt', { value: formatDate(nextProbeAt) }) }}
        </p>
        <p class="mt-2 border-t border-white/15 pt-2" data-testid="upstream-billing-probe-state">
          {{ t('admin.accounts.upstreamBilling.accountProbeState') }}
          <span :class="probeEnabled ? 'text-emerald-400' : 'text-red-400'">
            {{ probeEnabled ? t('admin.accounts.upstreamBilling.enabled') : t('admin.accounts.upstreamBilling.disabled') }}
          </span>
        </p>
        <p
          v-if="globalProbeEnabled === false"
          class="mt-1"
          data-testid="upstream-billing-global-probe-state"
        >
          {{ t('admin.accounts.upstreamBilling.globalProbeState') }}
          <span class="text-red-400">{{ t('admin.accounts.upstreamBilling.disabled') }}</span>
        </p>
      </div>
    </HelpTooltip>
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
    <div v-if="detailed && (stale || failureReason)" class="ratio__note" data-testid="upstream-billing-note">
      <span v-if="stale && Number.isFinite(receivedAt)">{{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: formatDate(snapshot?.received_at) }) }}</span>
      <span v-if="failureReason">{{ t('admin.accounts.table.ratioFailure', { reason: failureReason }) }}</span>
    </div>
  </div>
  <span v-else class="ratio__none">—</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { vTip } from '@/utils/tip'
import { upstreamRateView } from './upstreamBillingRate'
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
const failureReason = computed(() => view.value.failureReason)
const formatDate = (value?: string) => value
  ? new Date(value).toLocaleString(undefined, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  : '-'
</script>

<style scoped>
.ratio {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.ratio__v {
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  white-space: nowrap;
  cursor: help;
}
.ratio--stale .ratio__v,
.ratio__none {
  color: var(--color-text-muted);
}
.ratio__btn {
  display: inline-grid;
  width: 20px;
  height: 20px;
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
