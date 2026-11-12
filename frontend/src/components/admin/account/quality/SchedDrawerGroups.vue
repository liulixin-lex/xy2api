<template>
  <p v-if="!groups.length" class="sq-text-3 text-sm">—</p>
  <div v-else class="-mx-3 overflow-x-auto">
    <table class="sq-table">
      <thead>
        <tr>
          <th scope="col">{{ t('admin.schedQuality.col.group') }}</th>
          <th scope="col" class="w-[112px]">{{ t('admin.schedQuality.col.state') }}</th>
          <th v-for="dim in SCHED_DIMS" :key="dim" scope="col" class="sq-r w-[84px]">{{ t(`admin.schedQuality.dim.${dim}`) }}</th>
          <th scope="col" class="sq-r w-[96px]"><span class="sr-only">{{ t('admin.accounts.columns.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="group in groups"
          :key="group.group_id"
          :class="{ 'sq-target': group.group_id === targetGroupId }"
          :data-sq-group-row="group.group_id"
        >
          <td>
            <button type="button" class="sq-link sq-focus" :data-sq-trigger="`board:${group.group_id}`" @click="emit('board', group)">{{ group.name }}</button>
          </td>
          <td>
            <span :class="stateOf(group).cls">{{ stateOf(group).text }}</span>
          </td>
          <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num">
            <template v-for="cell in [cellOf(group, dim)]" :key="cell.kind">
              <span v-if="cell.kind === 'shape'" role="img" :aria-label="cell.label" :class="['sq-shape', `sq-shape--${cell.verdict}`]" />
              <span v-else v-tip="cell.tip" tabindex="0" :aria-label="cell.label" :class="['sq-focus', cell.cls]">{{ cell.text }}</span>
            </template>
          </td>
          <td class="sq-r">
            <span v-if="failedGroups.has(group.group_id)" class="sq-fail-word mr-2">{{ t('admin.schedQuality.result.failed') }}</span>
            <button
              type="button"
              class="btn btn-secondary sq-btn-xs sq-focus"
              :data-sq-trigger="`override:${group.group_id}`"
              @click="onAction(group, $event)"
            >{{ actionLabel(group) }}</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SchedAccountGroup, SchedGroupModelRow, SchedModelEvidence, SchedOverrideMode, SchedVerdict } from '@/types/schedQuality'
import { secondsUntil } from '@/composables/useSchedQuality'
import { causeDim, fmtCacheRatio, fmtCountdown, fmtErrorRate, fmtTtfo, SCHED_DIMS, verdictClass, type SchedDim } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// "各分组" table of the drawer (design spec B.4). A row on the same window as the cards above shows only verdict
// shapes (F1); a row on its own window (cooling restarted, just reset) shows its numbers coloured by its verdict;
// numbers from tests carry the test tip.
const props = defineProps<{
  groups: SchedAccountGroup[]
  model: string | null
  /** Evidence of the selected model (the cards above), if any. */
  card?: SchedModelEvidence
  now: number
  targetGroupId?: number | null
  failedGroups: Set<number>
}>()
const emit = defineEmits<{
  board: [group: SchedAccountGroup]
  override: [group: SchedAccountGroup, mode: SchedOverrideMode, anchor: HTMLElement]
  undo: [group: SchedAccountGroup]
}>()
const { t } = useI18n()

const rowOf = (group: SchedAccountGroup): SchedGroupModelRow | undefined => group.by_model.find((row) => row.model === props.model)

const hasValue = (row: SchedGroupModelRow, dim: SchedDim) =>
  dim === 'ttfo' ? row.ttfo.p90_ms !== null || row.ttfo.p90_timeout : dim === 'error' ? row.error.rate !== null : row.cache.ratio !== null

const cardHasData = (card?: SchedModelEvidence) =>
  !!card && (card.ttfo.p90_ms !== null || card.ttfo.p90_timeout || card.error.rate !== null || card.cache.ratio !== null)

// Same window as the cards: nothing comes from tests and every value and n matches.
function sameWindow(row: SchedGroupModelRow, card?: SchedModelEvidence) {
  if (!card || SCHED_DIMS.some((dim) => row[dim].from === 'test')) return false
  return row.ttfo.n === card.ttfo.n && row.ttfo.p90_ms === card.ttfo.p90_ms && row.ttfo.p90_timeout === card.ttfo.p90_timeout &&
    row.error.n === card.error.n && row.error.rate === card.error.rate &&
    row.cache.n === card.cache.turns && row.cache.ratio === card.cache.ratio
}

function stateOf(group: SchedAccountGroup): { text: string; cls: string } {
  const row = rowOf(group)
  if (row?.state === 'cooling') {
    if (row.testing) return { text: t('admin.schedQuality.state.testing'), cls: 'sq-test' }
    const left = secondsUntil(row.until, props.now)
    const word = t('admin.schedQuality.state.cooling')
    return { text: !row.waiting_test && left !== null && left > 0 ? `${word} ${fmtCountdown(left)}` : word, cls: 'sq-cool sq-num' }
  }
  const rowData = !!row && SCHED_DIMS.some((dim) => row[dim].from !== 'test' && hasValue(row, dim))
  if (!rowData && !cardHasData(props.card)) return { text: t('admin.schedQuality.state.nodata'), cls: 'sq-text-3' }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}

const valueText = (row: SchedGroupModelRow, dim: SchedDim) =>
  dim === 'ttfo' ? fmtTtfo(row.ttfo.p90_ms, row.ttfo.p90_timeout, t) : dim === 'error' ? fmtErrorRate(row.error.rate) : fmtCacheRatio(row.cache.ratio)

const dimName = (dim: SchedDim) => t(`admin.schedQuality.dim.${dim}`)
const verdictWord = (verdict: SchedVerdict) => t(`admin.schedQuality.aria.${verdict}`)

type Cell =
  | { kind: 'shape'; verdict: SchedVerdict; label: string }
  | { kind: 'value'; text: string; cls: string; tip: string; label: string }

function cellOf(group: SchedAccountGroup, dim: SchedDim): Cell {
  const row = rowOf(group)
  const shape = (verdict: SchedVerdict): Cell => ({ kind: 'shape', verdict, label: `${dimName(dim)} ${verdictWord(verdict)}` })
  if (!row) return shape('nodata')
  const own = row[dim]
  if (own.from === 'test') {
    const text = valueText(row, dim)
    const verdict = own.verdict
    return {
      kind: 'value',
      text,
      cls: verdict === 'nodata' ? 'sq-text' : verdictClass(verdict),
      tip: t('admin.schedQuality.tip.testRounds', { n: own.n }),
      label: verdict === 'nodata' ? `${dimName(dim)} ${text}` : t('admin.schedQuality.aria.cell', { dim: dimName(dim), value: text, verdict: verdictWord(verdict) })
    }
  }
  if (sameWindow(row, props.card)) return shape(own.verdict)
  if (!hasValue(row, dim)) {
    if (row.state === 'cooling' && causeDim(row.reason) === dim) return shape('fail')
    return { kind: 'value', text: '—', cls: 'sq-text-3', tip: t('admin.schedQuality.tip.samples', { n: own.n }), label: `${dimName(dim)} ${verdictWord('nodata')}` }
  }
  const text = valueText(row, dim)
  return {
    kind: 'value',
    text,
    cls: own.verdict === 'nodata' ? 'sq-text' : verdictClass(own.verdict),
    tip: t('admin.schedQuality.tip.samples', { n: own.n }),
    label: own.verdict === 'nodata' ? `${dimName(dim)} ${text}` : t('admin.schedQuality.aria.cell', { dim: dimName(dim), value: text, verdict: verdictWord(own.verdict) })
  }
}

function actionLabel(group: SchedAccountGroup) {
  const mode = group.override?.mode
  if (mode === 'exempt') return t('admin.schedQuality.action.undoExempt')
  if (mode === 'demote') return t('admin.schedQuality.action.undoDemote')
  return t(rowOf(group)?.state === 'cooling' ? 'admin.schedQuality.action.exempt' : 'admin.schedQuality.action.demote')
}

function onAction(group: SchedAccountGroup, event: MouseEvent) {
  if (group.override) {
    emit('undo', group)
    return
  }
  emit('override', group, rowOf(group)?.state === 'cooling' ? 'exempt' : 'demote', event.currentTarget as HTMLElement)
}
</script>
