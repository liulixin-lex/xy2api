<template>
  <div v-if="account.platform === 'openai'" class="iq" :class="{ 'iq--off': !enabled }" data-testid="iq-check-cell">
    <AccountSwitch :checked="enabled" :label="t('admin.accounts.iqCheck') + ' · ' + account.name" :disabled="disabled" :busy="busy" @toggle="emit('toggle', $event)" />
    <button
      v-if="lines.length"
      type="button"
      class="iq__lines"
      :aria-label="t('admin.accounts.iqRecords')"
      v-tip="tipCard"
      data-test="iq-lines"
      @click="emit('records')"
    >
      <template v-for="line in lines" :key="line.model">
        <span class="iq__m" data-testid="iq-target-tag">{{ line.name }}</span>
        <span class="iq__v" :class="`iq__v--${line.state}`">
          <Icon v-if="line.state === 'checking'" name="refresh" size="xs" class="iq__spin" />{{ t(`admin.accounts.table.iqStates.${line.state}`) }}
        </span>
      </template>
      <span v-if="more > 0" class="iq__more" data-test="iq-more">{{ t('admin.accounts.table.iqMore', { n: more }) }}</span>
    </button>
    <span v-else class="iq__none">—</span>
  </div>
  <span v-else class="iq__none">—</span>
</template>

<script setup lang="ts">
/**
 * IQ check column (spec 4.4, 7c, prototype): the switch and one line per model, at most two, then "+N". The words are
 * only smart (ok green), degraded (error red), checking (queued included, info blue), all at 500, and paused (the
 * secondary text, uncoloured) (12.3); the tip card uses the same tones. Unknown and disabled targets show "—". The records button has one tip card (12.7) listing every model, the folded
 * ones too, with its state, last check and recheck; a tap opens the records, which list every model with its recheck
 * (touch has no hover).
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { iqGateEntries, iqModelLabel, isIQBuiltinModel, sortIQTargets } from '@/components/admin/account/iqCheckPresentation'
import { currentTime } from '@/composables/useLiveClock'
import type { Account, IQTargetSummary } from '@/types'
import { vTip, type TipCard, type TipTone } from '@/utils/tip'
import AccountSwitch from './AccountSwitch.vue'
import { formatRemaining, formatShortTime, msLeft } from './accountTime'

const props = defineProps<{ account: Account; disabled?: boolean; busy?: boolean }>()
const emit = defineEmits<{ toggle: [enabled: boolean]; records: [] }>()
const { t } = useI18n()

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
// "+N" counts the rest (spec 7c).
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

const gates = computed(() => new Map(iqGateEntries(props.account.extra as Record<string, unknown> | undefined).map((gate) => [gate.model, gate.reset_at])))

/** When the model is checked again: the gate's end while it holds one, else its next run; none while checking or paused. */
function recheckAt(target: IQTargetSummary, state: LineState): string | null {
  if (state === 'checking' || state === 'paused') return null
  const gate = target.gate_active ? gates.value.get(target.model) : undefined
  return gate ?? target.next_run_at ?? null
}

const STATE_TONES: Record<LineState, TipTone | undefined> = { smart: 'ok', degraded: 'error', checking: 'info', paused: undefined }

/** Every model in check: full name, state, last check, recheck ("3m 后", "等待" once due, "—" checking or paused). */
function tipCard(): TipCard {
  return {
    head: [t('admin.accounts.table.tip.iqModel'), t('admin.accounts.table.tip.iqState'), t('admin.accounts.table.tip.iqLastRun'), t('admin.accounts.table.tip.iqRecheck')],
    rows: allLines.value.map(({ target, state }) => {
      const next = recheckAt(target, state)
      const left = next ? msLeft(next, currentTime()) : null
      return [
        target.model,
        { text: t(`admin.accounts.table.iqStates.${state}`), tone: STATE_TONES[state] },
        formatShortTime(target.last_run_at),
        !next ? '—' : left === null ? t('admin.accounts.table.tip.iqDue') : t('admin.accounts.table.tip.iqIn', { time: formatRemaining(t, left) })
      ]
    })
  }
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
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
  line-height: var(--line-height-body);
  white-space: nowrap;
}
.iq__v {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  white-space: nowrap;
}
/* The verdicts in their status colours at 500; paused is a neutral word (12.3). */
.iq__v--smart,
.iq__v--degraded,
.iq__v--checking {
  font-weight: var(--font-weight-medium);
}
.iq__v--smart {
  color: var(--color-status-ok-text);
}
.iq__v--degraded {
  color: var(--color-status-error-text);
}
.iq__v--checking {
  color: var(--color-status-info-text);
}
.iq__v--paused {
  color: var(--color-text-secondary);
}
.iq__more {
  grid-column: 1 / -1;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
/* 9px, 2px from its word: "检测中" fits the compact column whole (7m-1). In the word's colour (12.3). */
.iq__spin {
  width: 9px;
  height: 9px;
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
