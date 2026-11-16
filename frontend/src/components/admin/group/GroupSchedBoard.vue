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
            <th scope="col">{{ t('admin.schedQuality.col.pinned') }}</th>
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
            :class="{ 'sq-weak': unused(row), 'sq-split': unused(row) && index > 0 && !unused(data.rows[index - 1]) }"
            :data-sq-board-row="row.account_id"
          >
            <td class="sq-r sq-num sq-text-3">{{ unused(row) ? '—' : row.rank }}</td>
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
            <td>
              <span class="inline-flex items-center gap-2">
                <AccountSwitch
                  v-if="canManageGroups"
                  :checked="pinnedOf(row)"
                  :busy="pinBusy.has(row.account_id)"
                  :disabled="pinBusy.has(row.account_id)"
                  :label="t('admin.schedQuality.board.pinLabel', { name: row.name })"
                  :data-sq-pin="row.account_id"
                  @toggle="(next) => setPinned(row, next)"
                />
                <span v-else-if="row.pinned" data-sq-pinned>{{ t('admin.schedQuality.board.pinned') }}</span>
                <span v-else class="sq-text-3">{{ DASH }}</span>
                <span v-if="pinFailed.has(row.account_id)" class="sq-fail-word text-xs">{{ t('admin.schedQuality.result.failed') }}</span>
              </span>
            </td>
            <td class="sq-r sq-num">{{ row.priority }}</td>
            <td class="sq-r sq-num">{{ row.rate.toFixed(3) }}</td>
            <td class="sq-r sq-num">{{ row.in_flight }} / {{ row.concurrency }}</td>
            <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num">
              <!-- Each line's identity in this group; the board carries no numbers (the account's panel has the evidence). -->
              <span
                v-if="!stopped(row)"
                role="img"
                tabindex="0"
                :aria-label="`${t(`admin.schedQuality.dim.${dim}`)} ${t(`admin.schedQuality.identity.${row.lines[dim]}`)}`"
                v-tip="`${t(`admin.schedQuality.dim.${dim}`)} · ${t(`admin.schedQuality.identity.${row.lines[dim]}`)}`"
                :class="['sq-focus', IDENTITY_SHAPES[row.lines[dim]]]"
                :data-sq-line="dim"
                :data-identity="row.lines[dim]"
              >{{ row.lines[dim] === 'unknown' ? DASH : '' }}</span>
              <span v-else class="sq-text-3">{{ DASH }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import AccountSwitch from '@/components/admin/account/table/AccountSwitch.vue'
import SchedPauseMark from '@/components/admin/account/quality/SchedPauseMark.vue'
import { adminAPI } from '@/api/admin'
import type { BackendIdentity, BackendOrderRow } from '@/api/admin/schedQualityTypes'
import { SCHED_BOARD_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { useLiveClock } from '@/composables/useLiveClock'
import { closeSchedBoard, markSchedChanged, openSchedDrawer, qualityOpenerKey, setSchedBoardModel, useSchedLayers } from '@/composables/useSchedLayers'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { DASH, fmtCountdown, rejectName, SCHED_DIMS, secondsUntil } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// Group board (design spec C): the group's accounts in the backend's selection order (GET …/groups/:id/order, never
// re-sorted here), fetched every 5 s by the host page's live table while open (useSchedLayerData). The layers are
// front, trial (a free trial seat), trial with every seat taken, rear and stopped; pinned accounts lead their layer.
// Accounts the group does not use at all (stopped, or excluded by the rate range) come last in the secondary colour,
// with the reason. Each line shows its identity; a rear or trial row counts down to its end. Pinning needs
// groups:manage and is saved at once (PUT /admin/groups/:id/members/:account_id).
const { t, te } = useI18n()
const { board } = useSchedLayers()
const { board: boardData, boardKey, retry } = useSchedLayerData()
// The account link needs accounts:view; the name is plain text otherwise.
const { canView: canViewAccounts } = useAdminPermission('accounts')
const { canManage: canManageGroups } = useAdminPermission('groups')
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

const IDENTITY_SHAPES: Record<BackendIdentity, string> = {
  pass: 'sq-shape sq-shape--pass',
  rear: 'sq-shape sq-shape--fail',
  trial: 'sq-shape sq-shape--trial',
  unknown: 'sq-text-3'
}

const stopped = (row: BackendOrderRow) => row.layer === 'stopped'
/** Takes no part in the group: stopped, or outside the rate range. */
const unused = (row: BackendOrderRow) => stopped(row) || row.excluded !== null

function stateOf(row: BackendOrderRow): { text: string; cls: string } {
  const left = secondsUntil(row.until, now.value)
  const timed = (word: string) => (left ? `${word} ${fmtCountdown(left)}` : word)
  if (stopped(row)) return { text: timed(rejectName('not_schedulable', t, te)), cls: '' }
  if (row.excluded !== null) return { text: t('admin.schedQuality.board.excluded', { reason: rejectName(row.excluded, t, te) }), cls: '' }
  if (row.exempt) return { text: t('admin.schedQuality.state.exempted'), cls: 'sq-text' }
  const word = t(`admin.schedQuality.layer.${row.layer}`)
  if (row.layer === 'rear') return { text: timed(word), cls: 'sq-cool' }
  if (row.layer === 'trial' || row.layer === 'trial_full') return { text: timed(word), cls: 'sq-trial' }
  return { text: word, cls: 'sq-text' }
}

// ---- Pinning: saved at once; the switch shows the new state until the board's next answer ----
const pinBusy = reactive(new Set<number>())
const pinFailed = reactive(new Set<number>())
const pinShown = reactive(new Map<number, boolean>())
watch(() => boardData.data, () => pinShown.clear())
const pinnedOf = (row: BackendOrderRow) => pinShown.get(row.account_id) ?? row.pinned

async function setPinned(row: BackendOrderRow, pinned: boolean) {
  const groupId = board.groupId
  if (groupId === null || !canManageGroups.value || pinBusy.has(row.account_id)) return
  pinBusy.add(row.account_id)
  pinFailed.delete(row.account_id)
  try {
    const result = await adminAPI.groups.setMemberPinned(groupId, row.account_id, pinned)
    pinShown.set(row.account_id, result.pinned)
    void markSchedChanged()
  } catch {
    pinFailed.add(row.account_id)
    setTimeout(() => pinFailed.delete(row.account_id), 2000)
  } finally {
    pinBusy.delete(row.account_id)
  }
}
</script>
