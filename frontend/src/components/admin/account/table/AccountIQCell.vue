<template>
  <div v-if="account.platform === 'openai'" class="iq" :class="{ 'iq--off': !enabled }" data-testid="iq-check-cell">
    <AccountSwitch :checked="enabled" :label="t('admin.accounts.iqCheck') + ' · ' + account.name" :disabled="disabled" :busy="busy" @toggle="emit('toggle', $event)" />
    <button
      v-if="lines.length"
      type="button"
      class="iq__lines"
      :aria-label="t('admin.accounts.iqRecords')"
      data-test="iq-lines"
      @click="emit('records')"
    >
      <template v-for="line in lines" :key="line.model">
        <span class="iq__m" data-testid="iq-target-tag">{{ line.name }}</span>
        <span class="iq__v" :class="`iq__v--${line.state}`" v-tip="() => lineTip(line.target, line.state)">
          <Icon v-if="line.state === 'checking'" name="refresh" size="xs" class="iq__spin" />{{ t(`admin.accounts.table.iqStates.${line.state}`) }}
        </span>
      </template>
      <span v-if="more > 0" v-tip="allTip" class="iq__more" data-test="iq-more">{{ t('admin.accounts.table.iqMore', { n: more }) }}</span>
    </button>
    <span v-else class="iq__none">—</span>
  </div>
  <span v-else class="iq__none">—</span>
</template>

<script setup lang="ts">
/**
 * IQ check column (spec 4.4, 7c, prototype): the switch and one line per model, at most two, then "+N". The words are
 * only smart (secondary colour), degraded (amber, regular weight), checking (queued included) and paused (muted);
 * unknown and disabled targets show "—". The recheck time lives in the tip.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { iqGateEntries, iqModelLabel, isIQBuiltinModel, sortIQTargets } from '@/components/admin/account/iqCheckPresentation'
import { useLiveClock } from '@/composables/useLiveClock'
import type { Account, IQTargetSummary } from '@/types'
import { vTip } from '@/utils/tip'
import AccountSwitch from './AccountSwitch.vue'
import { formatRemaining, formatShortTime, msLeft } from './accountTime'

const props = defineProps<{ account: Account; disabled?: boolean; busy?: boolean }>()
const emit = defineEmits<{ toggle: [enabled: boolean]; records: [] }>()
const { t } = useI18n()
const now = useLiveClock()

const targets = computed(() => sortIQTargets(props.account.iq_targets ?? []))
const enabled = computed(() => targets.value.some((target) => isIQBuiltinModel(target.model) && target.enabled))

type LineState = 'smart' | 'degraded' | 'checking' | 'paused'
const stateOf = (target: IQTargetSummary): LineState | null => {
  if (target.state === 'running' || target.state === 'queued') return 'checking'
  if (target.gate_active || target.verdict === 'degraded') return 'degraded'
  if (target.state === 'paused') return 'paused'
  if (target.verdict === 'smart') return 'smart'
  return null
}

// Enabled targets, and disabled ones that still hold a gate or are being checked by hand. Two lines fit; with more,
// "+N" counts the rest and its tip lists every model with its state (spec 7c).
const allLines = computed(() =>
  targets.value
    .filter((target) => target.enabled || target.gate_active || target.state === 'queued' || target.state === 'running')
    .flatMap((target) => {
      const state = stateOf(target)
      return state ? [{ model: target.model, name: iqModelLabel(target.model), state, target }] : []
    })
)
const lines = computed(() => allLines.value.slice(0, 2))
const more = computed(() => allLines.value.length - lines.value.length)
const allTip = () =>
  allLines.value.map((line) => t('admin.accounts.table.iqTip', { model: line.target.model, state: t(`admin.accounts.table.iqStates.${line.state}`) })).join('\n')

const gates = computed(() => new Map(iqGateEntries(props.account.extra as Record<string, unknown> | undefined).map((gate) => [gate.model, gate.reset_at])))

function lineTip(target: IQTargetSummary, state: LineState): string {
  const head = t('admin.accounts.table.iqTip', { model: target.model, state: t(`admin.accounts.table.iqStates.${state}`) })
  if (state === 'checking') return `${head}\n${t('admin.accounts.table.iqChecking')}`
  const last = target.last_run_at ? t('admin.accounts.table.iqLastRun', { time: formatShortTime(target.last_run_at) }) : ''
  if (state === 'paused') return [[head, last].filter(Boolean).join(' · '), t('admin.accounts.table.iqPausedTip')].join('\n')
  const gate = target.gate_active ? gates.value.get(target.model) : undefined
  const next = gate ?? target.next_run_at
  const left = msLeft(next, now.value)
  const recheck = next ? (left === null ? t('admin.accounts.table.iqRecheckPending') : t('admin.accounts.table.iqRecheckIn', { time: formatRemaining(t, left) })) : ''
  return [[head, last].filter(Boolean).join(' · '), recheck].filter(Boolean).join('\n')
}
</script>

<style scoped>
.iq {
  display: flex;
  align-items: center;
  gap: 10px;
}
.iq__lines {
  display: grid;
  grid-template-columns: auto auto;
  column-gap: 6px;
  row-gap: 2px;
  align-items: center;
  justify-content: start;
  padding: 0;
  border: 0;
  background: none;
  cursor: pointer;
  text-align: left;
}
.iq__lines:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
.iq__m {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-body);
  white-space: nowrap;
}
.iq__v {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  white-space: nowrap;
}
.iq__v--smart,
.iq__v--checking {
  color: var(--color-text-secondary);
}
.iq__v--degraded {
  color: var(--color-status-back-text);
  font-weight: var(--font-weight-regular);
}
.iq__v--paused {
  color: var(--color-text-muted);
}
.iq__more {
  grid-column: 1 / -1;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
.iq__spin {
  width: 11px;
  height: 11px;
  color: var(--color-status-info);
  animation: spin 1.1s linear infinite;
}
.iq--off .iq__v,
.iq--off .iq__m {
  color: var(--color-text-muted);
}
.iq__none {
  color: var(--color-text-muted);
  font-size: var(--font-size-body);
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
