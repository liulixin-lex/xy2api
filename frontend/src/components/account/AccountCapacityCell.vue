<template>
  <!-- Neutral chips (prototype, 7e-14): one per limit, 15px lines when there are several. -->
  <div class="cap" :class="{ 'cap--multi': lines.length > 1 }" data-test="account-capacity">
    <span
      v-for="line in lines"
      :key="line.key"
      class="cap__chip"
      :class="{ 'cap__chip--zero': line.used === 0 && !line.full, 'cap__chip--full': line.full }"
      :data-cap="line.key"
      v-tip="line.tip"
    >
      <span v-if="lines.length > 1" class="cap__l">{{ line.label }}</span>
      <span class="cap__v">{{ line.current }} / {{ line.max }}</span>
      <template v-if="line.group != null">
        <span class="cap__v">/</span>
        <span class="cap__group" data-test="capacity-group-current">{{ line.group }}</span>
      </template>
      <span v-if="line.suffix" class="cap__suffix">{{ line.suffix }}</span>
    </span>
  </div>
</template>

<script setup lang="ts">
/**
 * Capacity column (spec 4.1, prototype): what is in use against each limit the account has. The lines and their tips
 * are the old badges' ones: concurrency always (with this group's share when the list is filtered to a group); for
 * Anthropic OAuth / setup-token the 5h window cost, sessions and RPM when limited; for API key and Bedrock the
 * daily, weekly and total quota when set. The look is the prototype's: a neutral chip with the UI font's tabular
 * digits, no icons (a short label names each line when there are several), greyed while nothing is in use, amber only
 * when the limit is reached. Money drops whole ".00" so a line fits the column; the tip has the exact amounts.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { vTip } from '@/utils/tip'

const props = defineProps<{
  account: Account
}>()

const { t } = useI18n()

interface CapLine {
  key: string
  label: string
  current: string | number
  max: string | number
  used: number
  full: boolean
  tip: string
  group?: number | null
  suffix?: string
}

const exactMoney = (value: number | null | undefined) => `$${(value ?? 0).toFixed(2)}`
/** $4.20, $30 (a whole amount without ".00"). */
const shortMoney = (value: number | null | undefined) => {
  const cents = Math.round((value ?? 0) * 100)
  return cents % 100 === 0 ? `$${cents / 100}` : `$${(cents / 100).toFixed(2)}`
}
const withAmounts = (tip: string, used: number, limit: number) => [tip, `${exactMoney(used)} / ${exactMoney(limit)}`].filter(Boolean).join('\n')

const isAnthropicOAuthOrSetupToken = computed(() =>
  props.account.platform === 'anthropic' && (props.account.type === 'oauth' || props.account.type === 'setup-token')
)
const isQuotaEligible = computed(() => props.account.type === 'apikey' || props.account.type === 'bedrock')
const limited = (value: number | null | undefined): value is number => value != null && value > 0

function concurrencyLine(): CapLine {
  const current = props.account.current_concurrency || 0
  const max = props.account.concurrency
  // Only a list filtered to one group carries the group's share.
  const group = props.account.current_group_concurrency
  return {
    key: 'concurrency',
    label: t('admin.accounts.capacity.labels.concurrency'),
    current,
    max,
    used: current,
    full: current >= max,
    group: group ?? null,
    tip: group == null ? '' : t('admin.accounts.capacity.concurrencyGroup', { current, max, group })
  }
}

function windowCostLine(): CapLine | null {
  const limit = props.account.window_cost_limit
  if (!isAnthropicOAuthOrSetupToken.value || !limited(limit)) return null
  const current = props.account.current_window_cost ?? 0
  const reserve = props.account.window_cost_sticky_reserve || 10
  const tip = current >= limit + reserve
    ? t('admin.accounts.capacity.windowCost.blocked')
    : current >= limit ? t('admin.accounts.capacity.windowCost.stickyOnly') : t('admin.accounts.capacity.windowCost.normal')
  return {
    key: 'window_cost',
    label: t('admin.accounts.capacity.labels.windowCost'),
    current: shortMoney(current),
    max: shortMoney(limit),
    used: current,
    full: current >= limit,
    tip: withAmounts(tip, current, limit)
  }
}

function sessionsLine(): CapLine | null {
  const max = props.account.max_sessions
  if (!isAnthropicOAuthOrSetupToken.value || !limited(max)) return null
  const current = props.account.active_sessions ?? 0
  const idle = props.account.session_idle_timeout_minutes || 5
  return {
    key: 'sessions',
    label: t('admin.accounts.capacity.labels.sessions'),
    current,
    max,
    used: current,
    full: current >= max,
    tip: current >= max ? t('admin.accounts.capacity.sessions.full', { idle }) : t('admin.accounts.capacity.sessions.normal', { idle })
  }
}

function rpmLine(): CapLine | null {
  const base = props.account.base_rpm
  if (!isAnthropicOAuthOrSetupToken.value || !limited(base)) return null
  const current = props.account.current_rpm ?? 0
  const tiered = (props.account.rpm_strategy || 'tiered') !== 'sticky_exempt'
  const buffer = props.account.rpm_sticky_buffer ?? Math.max(1, Math.floor(base / 5))
  let tip: string
  if (tiered) {
    if (current >= base + buffer) tip = t('admin.accounts.capacity.rpm.tieredBlocked', { buffer })
    else if (current >= base) tip = t('admin.accounts.capacity.rpm.tieredStickyOnly', { buffer })
    else if (current >= base * 0.8) tip = t('admin.accounts.capacity.rpm.tieredWarning')
    else tip = t('admin.accounts.capacity.rpm.tieredNormal')
  } else if (current >= base) tip = t('admin.accounts.capacity.rpm.stickyExemptOver')
  else if (current >= base * 0.8) tip = t('admin.accounts.capacity.rpm.stickyExemptWarning')
  else tip = t('admin.accounts.capacity.rpm.stickyExemptNormal')
  return {
    key: 'rpm',
    label: t('admin.accounts.capacity.labels.rpm'),
    current,
    max: base,
    used: current,
    full: current >= base,
    suffix: tiered ? '[T]' : '[S]',
    tip
  }
}

function quotaLine(key: string, label: string, used: number | null | undefined, limit: number | null | undefined): CapLine | null {
  if (!isQuotaEligible.value || !limited(limit)) return null
  const value = used ?? 0
  const tip = value >= limit ? t('admin.accounts.capacity.quota.exceeded') : t('admin.accounts.capacity.quota.normal')
  return { key, label, current: shortMoney(value), max: shortMoney(limit), used: value, full: value >= limit, tip: withAmounts(tip, value, limit) }
}

const lines = computed<CapLine[]>(() => {
  const a = props.account
  return [
    concurrencyLine(),
    windowCostLine(),
    sessionsLine(),
    rpmLine(),
    quotaLine('quota_daily', 'D', a.quota_daily_used, a.quota_daily_limit),
    quotaLine('quota_weekly', 'W', a.quota_weekly_used, a.quota_weekly_limit),
    quotaLine('quota_total', t('admin.accounts.capacity.labels.total'), a.quota_used, a.quota_limit)
  ].filter((line): line is CapLine => line !== null)
})
</script>

<style scoped>
.cap {
  display: grid;
  justify-items: start;
  gap: 1px;
  min-width: 0;
}
.cap__chip {
  display: inline-flex;
  height: 20px;
  max-width: 100%;
  align-items: center;
  gap: 4px;
  padding: 0 6px;
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
  line-height: 16px;
  white-space: nowrap;
}
.cap--multi .cap__chip {
  height: 15px;
  line-height: 15px;
}
.cap__l,
.cap__group,
.cap__suffix {
  color: var(--color-text-secondary);
}
.cap__chip--zero,
.cap__chip--zero .cap__l,
.cap__chip--zero .cap__group,
.cap__chip--zero .cap__suffix {
  color: var(--color-text-muted-on-pill);
}
.cap__chip--full .cap__v {
  color: var(--color-status-back-text);
}
</style>
