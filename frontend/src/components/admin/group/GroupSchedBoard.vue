<template>
  <BaseDialog
    ref="shellRef"
    :show="board.groupId !== null"
    :title="board.groupName"
    width="wide"
    :close-on-click-outside="true"
    :raise-key="board.raise"
    @close="closeSchedBoard"
  >
    <template #title>
      <span class="truncate">{{ board.groupName }}</span>
      <SchedPauseMark v-if="data" :global-enabled="data.global_enabled" :stale="data.stale" />
    </template>
    <template #header-actions>
      <Select
        v-if="modelOptions.length"
        :model-value="data?.model ?? null"
        :options="modelOptions"
        :searchable="false"
        :aria-label="t('admin.schedQuality.aria.model')"
        class="sq-select-sm w-[150px]"
        @update:model-value="(value) => setSchedBoardModel(String(value))"
      />
    </template>

    <div v-if="!data && !failed" data-sq-board-loading>
      <span v-for="i in 6" :key="i" :class="['skeleton block h-3.5 w-full', i === 1 ? '' : 'mt-4']" />
    </div>
    <div v-else-if="!data" class="flex items-center gap-3" data-sq-board-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="retry(SCHED_BOARD_TASK)">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <p v-else-if="!data.rows.length" class="sq-text-3 text-sm">{{ t('admin.schedQuality.state.nodata') }}</p>
    <div v-else class="-mx-3 overflow-x-auto">
      <table class="sq-table" data-sq-board-table>
        <thead>
          <tr>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.rank') }}</th>
            <th scope="col">{{ t('admin.schedQuality.col.account') }}</th>
            <th scope="col">{{ t('admin.schedQuality.col.state') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.drawer.priority') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.drawer.rate') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.drawer.concurrency') }}</th>
            <th v-for="dim in SCHED_DIMS" :key="dim" scope="col" class="sq-r">{{ t(`admin.schedQuality.dim.${dim}`) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, index) in data.rows"
            :key="row.account_id"
            :class="{ 'sq-weak': stopped(row), 'sq-split': stopped(row) && index > 0 && !stopped(data.rows[index - 1]) }"
            :data-sq-board-row="row.account_id"
          >
            <td class="sq-r sq-num sq-text-3">{{ stopped(row) ? '—' : row.rank }}</td>
            <td>
              <button
                v-if="canViewAccounts"
                type="button"
                class="sq-link sq-focus"
                :data-sq-trigger="`account:${row.account_id}`"
                :data-quality-opener="qualityOpenerKey(row.account_id)"
                @click="openSchedDrawer({ id: row.account_id, name: row.name }, { model: data.model || null })"
              >{{ row.name }}</button>
              <template v-else>{{ row.name }}</template>
            </td>
            <td>
              <span :class="stateOf(row).cls" data-sq-state>{{ stateOf(row).text }}</span>
            </td>
            <td class="sq-r sq-num">{{ row.priority }}</td>
            <td class="sq-r sq-num">{{ row.rate.toFixed(3) }}</td>
            <td class="sq-r sq-num">{{ row.in_flight }} / {{ row.concurrency }}</td>
            <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num">
              <!-- The backend names the failing dimensions of a row at the back; it sends no numbers for the board. -->
              <span v-if="failing(row, dim)" role="img" :aria-label="`${t(`admin.schedQuality.dim.${dim}`)} ${t('admin.schedQuality.aria.fail')}`" class="sq-shape sq-shape--fail" />
              <span v-else tabindex="0" :aria-label="`${t(`admin.schedQuality.dim.${dim}`)} ${t('admin.schedQuality.aria.nodata')}`" class="sq-focus sq-text-3">{{ DASH }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import SchedPauseMark from '@/components/admin/account/quality/SchedPauseMark.vue'
import type { BackendDim, BackendOrderRow } from '@/api/admin/schedQualityTypes'
import { SCHED_BOARD_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { useLiveClock } from '@/composables/useLiveClock'
import { closeSchedBoard, openSchedDrawer, qualityOpenerKey, setSchedBoardModel, useSchedLayers } from '@/composables/useSchedLayers'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { DASH, fmtCountdown, rejectName, SCHED_DIMS, secondsUntil } from '@/utils/schedQualityFormat'

// Group board (design spec C): the group's accounts in the backend's selection order (GET …/groups/:id/order, never
// re-sorted here), fetched every 5 s by the host page's live table while open (useSchedLayerData). Accounts that take
// no part (layer stopped) come last in the secondary colour; a row at the back (layer below) counts down to its end
// and marks its failing dimensions.
const { t, te } = useI18n()
const { board } = useSchedLayers()
const { board: boardData, boardKey, retry } = useSchedLayerData()
// The account link needs accounts:view; the name is plain text otherwise.
const { canView: canViewAccounts } = useAdminPermission('accounts')
const now = useLiveClock()
const shellRef = ref<{ holdFocus: () => () => void } | null>(null)

// The data of the group and model asked for (the opener's model, then whatever is picked here; the API answers with
// the model it used).
const data = computed(() => (boardData.key !== null && boardData.key === boardKey() ? boardData.data : null))
const failed = computed(() => boardData.failedKey !== null && boardData.failedKey === boardKey())

let restoreFocus: (() => void) | null = null
watch(() => boardData.data, () => { restoreFocus = shellRef.value?.holdFocus() ?? null }, { flush: 'pre' })
watch(() => boardData.data, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

const modelOptions = computed(() => (data.value?.models ?? []).map((m) => ({ value: m, label: m })))

const stopped = (row: BackendOrderRow) => row.layer === 'stopped'
const failing = (row: BackendOrderRow, dim: BackendDim) => !stopped(row) && row.dims.includes(dim)

function stateOf(row: BackendOrderRow): { text: string; cls: string } {
  const left = secondsUntil(row.until, now.value)
  if (stopped(row)) {
    const word = rejectName('not_schedulable', t, te)
    return { text: left ? `${word} ${fmtCountdown(left)}` : word, cls: '' }
  }
  if (row.exempt) return { text: t('admin.schedQuality.state.exempted'), cls: 'sq-text' }
  if (row.layer === 'below') {
    const word = t('admin.schedQuality.state.cooling')
    return { text: left ? `${word} ${fmtCountdown(left)}` : word, cls: 'sq-cool' }
  }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}
</script>
