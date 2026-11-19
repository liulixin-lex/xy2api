<template>
  <dl class="pdet" :data-sq-perf-detail="view.key">
    <div v-for="item in items" :key="item.key" class="pdet__row" :data-sq-perf-detail-item="item.key">
      <dt class="pdet__k">{{ item.label }}</dt>
      <dd class="pdet__v">
        <span v-for="(line, index) in item.lines" :key="index" class="pdet__line">{{ line }}</span>
      </dd>
    </div>
  </dl>
</template>

<script setup lang="ts">
/**
 * One 12-hour performance row opened (the board's 12-hour view and the panel's "过去 12 小时"): every figure of the row
 * in plain sentences, then each line's latest assessment. A figure short of its minimum is told as counts; what the
 * backend did not send is left out, not written as unknown. `samples` (the panel has them, from the account's detail)
 * adds when a rear line's evidence all came from other groups.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendDim, BackendSampleCounts } from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { SCHED_DIMS, schedWord } from '@/utils/schedQualityFormat'
import {
  evidenceElsewhere,
  fmtAmount,
  fmtCount,
  fmtPct,
  fmtScore,
  fmtSigned,
  MIN_ATTEMPTS,
  MIN_FIRST,
  MIN_FIRST_SESSIONS,
  MIN_TURNS,
  MIN_TURN_SESSIONS,
  noActionLong,
  SWITCH_REASONS,
  type PerfView
} from './performance'

const props = defineProps<{
  view: PerfView
  samples?: Partial<Record<BackendDim, BackendSampleCounts>>
}>()
const { t, te } = useI18n()
const T = (key: string, values?: Record<string, unknown>) => t(`admin.schedQuality.perf.detail.${key}`, values ?? {})

function firstLines(): string[] {
  const { row, group } = props.view
  const { first } = row
  if (first.n === 0 && first.unknown === 0) return [T('firstNone')]
  const lines = [T('firstLine', { n: fmtCount(first.n), slow: fmtCount(first.slow), s: group?.ttft_seconds ?? '—' })]
  if (first.unknown > 0) lines.push(T('firstUnknown', { n: fmtCount(first.unknown) }))
  const { firstEnough, expectedRate, expectedEnough, extra } = props.view
  if (!firstEnough) lines.push(T('firstShort', { min: MIN_FIRST, sessions: MIN_FIRST_SESSIONS }))
  else if (expectedRate === null) lines.push(T('firstNoPeers'))
  else if (!expectedEnough) lines.push(T('firstFewPeers', { n: fmtCount(first.expected_n) }))
  else lines.push(T('firstExpected', { expected: fmtCount(Math.round(first.n * expectedRate)), rate: fmtPct(expectedRate), extra: fmtSigned(extra ?? 0) }))
  return lines
}

function cacheLines(): string[] {
  const { row, group, cacheRate } = props.view
  const { cont } = row
  if (cont.turns === 0) return [T('cacheNone')]
  if (cacheRate === null) {
    return [T('cacheShort', { turns: fmtCount(cont.turns), sessions: fmtCount(cont.sessions), minTurns: MIN_TURNS, minSessions: MIN_TURN_SESSIONS })]
  }
  return [T('cacheLine', { turns: fmtCount(cont.turns), sessions: fmtCount(cont.sessions), rate: fmtPct(cacheRate), min: group ? fmtPct(group.min_cache_ratio, 0) : '—' })]
}

function timeoutLines(): string[] {
  const { row, timeoutRate } = props.view
  const { attempts } = row
  const values = { n: fmtCount(attempts.n), k: fmtCount(attempts.timeouts) }
  const lines = [timeoutRate === null ? T('timeoutsShort', { ...values, min: MIN_ATTEMPTS }) : T('timeoutsLine', { ...values, rate: fmtPct(timeoutRate) })]
  if (attempts.failures > 0) lines.push(T('failures', { n: fmtCount(attempts.failures) }))
  return lines
}

function switchLines(): string[] {
  const { row, switchTotal } = props.view
  if (switchTotal === 0) return [T('switchesNone')]
  const reasons = SWITCH_REASONS.map((reason) =>
    T('switchItem', { reason: schedWord('perf.switchReason', reason, t, te), n: fmtCount(row.switches?.[reason] ?? 0) }))
  return [T('switchesLine', { n: fmtCount(switchTotal), reasons: reasons.join(' · ') })]
}

function scoreLines(): string[] {
  const { row, rank } = props.view
  const { score } = row
  if (score.value === null) {
    return [T('scoreNone', { n: fmtCount(score.n), sessions: fmtCount(score.sessions), min: MIN_FIRST, minSessions: MIN_FIRST_SESSIONS })]
  }
  const lines = [T('scoreLine', { value: fmtScore(score.value), n: fmtCount(score.n), sessions: fmtCount(score.sessions), excess: fmtAmount(score.excess) })]
  if (rank) lines.push(T('scoreRank', { place: rank.place, of: rank.of }))
  lines.push(t('admin.schedQuality.perf.scoreUseFull'))
  return lines
}

function verdictLines(): string[] {
  const { row, group } = props.view
  if (!row.member) return [T('notMember')]
  if (group?.gate_enabled === false) return [T('gateOff')]
  return SCHED_DIMS.flatMap((dim) => {
    const line = row.lines[dim]
    const name = t(`admin.schedQuality.lineName.${dim}`)
    const latest = line?.last_eval ?? null
    const lines: string[] = []
    if (!latest) lines.push(T('evalNone', { line: name }))
    else if (latest.acted) {
      const action = schedWord('decisions.action', latest.action, t, te)
      lines.push(T(latest.reason === 'over' ? 'evalOver' : 'evalActed', { line: name, time: formatShortTime(latest.at), action }))
    } else {
      const reason = noActionLong(latest, t, te, { trialEnd: line?.identity === 'trial' ? line.until : null, formatTime: formatShortTime })
      lines.push(T('evalNoAction', { line: name, time: formatShortTime(latest.at), reason }))
    }
    if (line && evidenceElsewhere(line.identity, props.samples?.[dim])) lines.push(T('elsewhere', { line: name }))
    return lines
  })
}

const items = computed(() => {
  const list = [
    { key: 'first', lines: firstLines() },
    { key: 'cache', lines: cacheLines() },
    ...(props.view.row.attempts.n > 0 ? [{ key: 'timeouts', lines: timeoutLines() }] : []),
    { key: 'switches', lines: switchLines() },
    { key: 'score', lines: scoreLines() },
    { key: 'verdict', lines: verdictLines() }
  ]
  return list.map((item) => ({ ...item, label: T(item.key) }))
})
</script>

<style scoped>
/* One label column as wide as the widest label, the sentences beside it. */
.pdet {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 12px;
  margin: 0;
  font-size: 13px;
  line-height: 20px;
}
.pdet__row {
  display: contents;
}
.pdet__k {
  color: var(--sq-text-3);
}
.pdet__v {
  display: grid;
  min-width: 0;
  margin: 0;
  color: var(--sq-text);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
  white-space: normal;
}
</style>
