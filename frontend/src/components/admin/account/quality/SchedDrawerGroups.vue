<template>
  <p v-if="!groups.length" class="sq-text-3 text-sm">—</p>
  <div v-else class="-mx-3 overflow-x-auto">
    <table class="sq-table table-fixed min-w-[600px]">
      <colgroup>
        <col />
        <col class="w-[112px]" />
        <col v-for="dim in SCHED_DIMS" :key="dim" class="w-[84px]" />
        <col class="w-[96px]" />
      </colgroup>
      <thead>
        <tr>
          <th scope="col">{{ t('admin.schedQuality.col.group') }}</th>
          <th scope="col">{{ t('admin.schedQuality.col.state') }}</th>
          <th v-for="dim in SCHED_DIMS" :key="dim" scope="col" class="sq-r">{{ t(`admin.schedQuality.dim.${dim}`) }}</th>
          <th scope="col" class="sq-r"><span class="sr-only">{{ t('admin.accounts.columns.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="group in groups"
          :key="group.group_id"
          :class="{ 'sq-target': group.group_id === targetGroupId }"
          :data-sq-group-row="group.group_id"
        >
          <!-- Every row shows the override in effect for its group next to the name, to every reader; the action
               beside it needs accounts:manage. -->
          <td>
            <div class="flex min-w-0 items-center gap-1.5">
              <button v-if="canViewGroups" type="button" class="sq-link sq-focus min-w-0 truncate" :data-sq-trigger="`board:${group.group_id}`" @click="emit('board', group)">{{ group.name }}</button>
              <span v-else class="min-w-0 truncate">{{ group.name }}</span>
              <SchedOverrideCapsule v-if="group.override" class="shrink-0" />
            </div>
          </td>
          <!-- A group without state rows: no state word and no numbers (contract delta D3). -->
          <template v-if="!group.by_model.length">
            <td />
            <td v-for="dim in SCHED_DIMS" :key="dim" />
          </template>
          <template v-else>
            <td>
              <span :class="stateOf(group).cls">{{ stateOf(group).text }}</span>
            </td>
            <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num">
              <template v-for="cell in [cellOf(group, dim)]" :key="cell.kind">
                <span v-if="cell.kind === 'shape'" role="img" :aria-label="cell.label" :class="['sq-shape', `sq-shape--${cell.verdict}`]" />
                <span v-else v-tip="cell.tip" tabindex="0" :aria-label="cell.label" :class="['sq-focus', cell.cls]">{{ cell.text }}</span>
              </template>
            </td>
          </template>
          <td class="sq-r">
            <span v-if="failedGroups.has(group.group_id)" class="sq-fail-word mr-2">{{ t('admin.schedQuality.result.failed') }}</span>
            <button
              v-if="canManage"
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
import SchedOverrideCapsule from './SchedOverrideCapsule.vue'
import type { SchedAccountGroup, SchedGroupModelRow, SchedModelEvidence, SchedVerdict } from '@/types/schedQuality'
import { secondsUntil } from '@/composables/useSchedQuality'
import { useAdminPermission } from '@/composables/useAdminPermission'
import {
  causeDim, fmtCacheRatio, fmtCountdown, fmtErrorRate, fmtTtfo, known, SCHED_DIMS, SCHED_STATES, SCHED_VERDICTS, verdictClass, type SchedDim
} from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// "各分组" table of the drawer (design spec B.4). A row on the same window as the cards above shows only verdict
// shapes (F1); a row on its own window (cooling restarted, just reset) shows its numbers coloured by its verdict.
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
  override: [group: SchedAccountGroup, anchor: HTMLElement]
  undo: [group: SchedAccountGroup]
}>()
const { t } = useI18n()
// The group link needs groups:view (plain name otherwise); the row button needs accounts:manage.
const { canView: canViewGroups } = useAdminPermission('groups')
const { canManage } = useAdminPermission('accounts')

const rowOf = (group: SchedAccountGroup): SchedGroupModelRow | undefined => group.by_model.find((row) => row.model === props.model)

const hasValue = (row: SchedGroupModelRow, dim: SchedDim) =>
  dim === 'ttfo' ? row.ttfo.p90_ms !== null || row.ttfo.p90_timeout : dim === 'error' ? row.error.rate !== null : row.cache.ratio !== null

const cardHasData = (card?: SchedModelEvidence) =>
  !!card && (card.ttfo.p90_ms !== null || card.ttfo.p90_timeout || card.error.rate !== null || card.cache.ratio !== null)

// Same window as the cards: every value and n matches.
function sameWindow(row: SchedGroupModelRow, card?: SchedModelEvidence) {
  if (!card) return false
  return row.ttfo.n === card.ttfo.n && row.ttfo.p90_ms === card.ttfo.p90_ms && row.ttfo.p90_timeout === card.ttfo.p90_timeout &&
    row.error.n === card.error.n && row.error.rate === card.error.rate &&
    row.cache.n === card.cache.turns && row.cache.ratio === card.cache.ratio
}

function stateOf(group: SchedAccountGroup): { text: string; cls: string } {
  const row = rowOf(group)
  if (row && !known(row.state, SCHED_STATES)) return { text: t('admin.schedQuality.unknown'), cls: 'sq-text-3' }
  if (row?.state === 'cooling') {
    const left = secondsUntil(row.until, props.now)
    const word = t('admin.schedQuality.state.cooling')
    return { text: left !== null && left > 0 ? `${word} ${fmtCountdown(left)}` : word, cls: 'sq-cool sq-num' }
  }
  const rowData = !!row && SCHED_DIMS.some((dim) => hasValue(row, dim))
  if (!rowData && !cardHasData(props.card)) return { text: t('admin.schedQuality.state.nodata'), cls: 'sq-text-3' }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}

const valueText = (row: SchedGroupModelRow, dim: SchedDim) =>
  dim === 'ttfo' ? fmtTtfo(row.ttfo.p90_ms, row.ttfo.p90_timeout, t) : dim === 'error' ? fmtErrorRate(row.error.rate) : fmtCacheRatio(row.cache.ratio)

const dimName = (dim: SchedDim) => t(`admin.schedQuality.dim.${dim}`)
const verdictWord = (verdict: SchedVerdict | null) => t(verdict ? `admin.schedQuality.aria.${verdict}` : 'admin.schedQuality.unknown')

type Cell =
  | { kind: 'shape'; verdict: SchedVerdict; label: string }
  | { kind: 'value'; text: string; cls: string; tip: string; label: string }

function cellOf(group: SchedAccountGroup, dim: SchedDim): Cell {
  const row = rowOf(group)
  const shape = (verdict: SchedVerdict): Cell => ({ kind: 'shape', verdict, label: `${dimName(dim)} ${verdictWord(verdict)}` })
  if (!row) return shape('nodata')
  const own = row[dim]
  // An unknown verdict is neutral: the number uncoloured, or the fallback word instead of a shape.
  const verdict = known(own.verdict, SCHED_VERDICTS)
  const value = (text: string, tip: string): Cell => ({
    kind: 'value',
    text,
    cls: verdict && verdict !== 'nodata' ? verdictClass(verdict) : 'sq-text',
    tip,
    label: verdict === 'nodata' ? `${dimName(dim)} ${text}` : t('admin.schedQuality.aria.cell', { dim: dimName(dim), value: text, verdict: verdictWord(verdict) })
  })
  const other = (): Cell => {
    const word = verdictWord(null)
    return { kind: 'value', text: word, cls: 'sq-text-3', tip: '', label: `${dimName(dim)} ${word}` }
  }
  if (sameWindow(row, props.card)) return verdict ? shape(verdict) : other()
  if (!hasValue(row, dim)) {
    if (!verdict) return other()
    if (row.state === 'cooling' && causeDim(row.reason) === dim) return shape('fail')
    return { kind: 'value', text: '—', cls: 'sq-text-3', tip: t('admin.schedQuality.tip.samples', { n: own.n }), label: `${dimName(dim)} ${verdictWord('nodata')}` }
  }
  return value(valueText(row, dim), t('admin.schedQuality.tip.samples', { n: own.n }))
}

const actionLabel = (group: SchedAccountGroup) =>
  t(group.override ? 'admin.schedQuality.action.undoExempt' : 'admin.schedQuality.action.exempt')

function onAction(group: SchedAccountGroup, event: MouseEvent) {
  if (group.override) {
    emit('undo', group)
    return
  }
  emit('override', group, event.currentTarget as HTMLElement)
}
</script>
