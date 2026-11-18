<template>
  <BaseDialog
    ref="shellRef"
    :show="board.groupId !== null"
    :title="board.groupName"
    width="extra-wide"
    :close-on-click-outside="true"
    :raise-key="board.raise"
    @close="closeSchedBoard"
  >
    <template #title>
      <span class="truncate">{{ board.groupName }}</span>
      <SchedPauseMark v-if="data" :global-enabled="data.global_enabled" :stale="data.stale" />
    </template>
    <template #header-actions>
      <!-- The models with samples, or any model name typed in (one declared only by accounts that are off has no traffic). -->
      <Select
        :model-value="data?.model ?? board.model ?? null"
        :options="modelOptions"
        searchable
        creatable
        :creatable-prefix="t('admin.schedQuality.board.modelInput')"
        :search-placeholder="t('admin.schedQuality.board.modelSearch')"
        :aria-label="t('admin.schedQuality.aria.model')"
        class="sq-select-sm w-[180px]"
        data-sq-board-model
        @update:model-value="pickModel"
      />
    </template>

    <!-- The group over all its models (H.9): identities of its account × model keys, the users' waits, migration 400s. -->
    <dl v-if="data" class="mb-4 grid gap-1 text-[13px]" data-sq-board-summary>
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <dt class="sq-text-3">{{ t('admin.schedQuality.board.summaryTitle') }}</dt>
        <dd v-tip="t('admin.schedQuality.board.summaryCountsTip')" class="sq-num sq-text" tabindex="0" data-sq-summary="accounts">{{ summaryText.counts }}</dd>
      </div>
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <dt class="sq-text-3">{{ summaryText.wait }}</dt>
        <dd class="sq-num sq-text" data-sq-summary="e2e">{{ summaryText.e2e }}</dd>
        <dd class="sq-num sq-text" data-sq-summary="body">{{ summaryText.body }}</dd>
        <dd class="sq-num sq-text" data-sq-summary="migration">{{ summaryText.migration }}</dd>
      </div>
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <dt v-tip="t('admin.schedQuality.board.rearEntriesTip')" class="sq-text-3" tabindex="0">{{ t('admin.schedQuality.board.rearEntriesTitle') }}</dt>
        <dd class="sq-num sq-text" data-sq-summary="rear-entries">{{ summaryText.rearEntries }}</dd>
      </div>
    </dl>
    <!-- The picked model's supply in the group: declared, enabled, eligible now on the default entry (free slots not
         included); unknown when not read. -->
    <dl v-if="data" class="mb-4 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-[13px]" data-sq-board-supply>
      <dt v-tip="t('admin.schedQuality.board.supplyTip')" class="sq-text-3" tabindex="0">{{ t('admin.schedQuality.board.supplyTitle', { model: data.model }) }}</dt>
      <dd class="sq-num sq-text" data-sq-supply="counts">{{ supplyText.counts }}</dd>
      <dd v-if="supplyText.at" class="sq-num sq-text-3" data-sq-supply="at">{{ supplyText.at }}</dd>
      <dd v-else class="sq-text-3" data-sq-supply="failed">{{ supplyText.why }}</dd>
    </dl>
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
                  :busy="pinBusy.has(rowKey(row))"
                  :disabled="pinBusy.has(rowKey(row))"
                  :label="t('admin.schedQuality.board.pinLabel', { name: row.name })"
                  :data-sq-pin="row.account_id"
                  @toggle="(next) => setPinned(row, next)"
                />
                <span v-else-if="row.pinned" data-sq-pinned>{{ t('admin.schedQuality.board.pinned') }}</span>
                <span v-else class="sq-text-3">{{ DASH }}</span>
                <!-- Only the word, so the column stays narrow at every width (1024 included); the tip has the backend's reason. -->
                <span
                  v-if="pinFailedOf(row) !== null"
                  v-tip="pinFailedOf(row)"
                  class="sq-fail-word whitespace-nowrap text-xs"
                  role="status"
                  tabindex="0"
                  :aria-label="[t('admin.schedQuality.result.failed'), pinFailedOf(row)].filter(Boolean).join(' · ')"
                  :data-sq-pin-failed="row.account_id"
                >{{ t('admin.schedQuality.result.failed') }}</span>
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
                :aria-label="`${t(`admin.schedQuality.dim.${dim}`)} ${word('identity', row.lines[dim])}`"
                v-tip="`${t(`admin.schedQuality.dim.${dim}`)} · ${word('identity', row.lines[dim])}`"
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
import { DASH, fmtCountdown, rejectName, SCHED_DIMS, schedWord, secondsUntil } from '@/utils/schedQualityFormat'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatRecentSeconds } from '@/components/admin/account/table/accountRecent'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { vTip } from '@/utils/tip'

// Group board (design spec C): the group's accounts in the backend's selection order (GET …/groups/:id/order, never
// re-sorted here), fetched every 5 s by the host page's live table while open (useSchedLayerData). The layers are
// front, trial (a free trial seat), trial with every seat taken, rear and stopped; pinned accounts lead their layer.
// Accounts the group does not use at all (stopped, or excluded by the rate range) come last in the secondary colour,
// with the reason. Each line shows its identity; a rear or trial row counts down to its end. Pinning needs
// groups:manage and is saved at once (PUT /admin/groups/:id/members/:account_id).
const { t, te } = useI18n()
const word = (table: string, value: string) => schedWord(table, value, t, te)
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

// The models of the group's last answer: kept while another model loads or fails, so a name typed in can be changed.
const lastModels = ref<{ groupId: number; models: string[] } | null>(null)
watch(data, (value) => { if (value) lastModels.value = { groupId: value.group_id, models: value.models } })
const modelOptions = computed(() => {
  const models = lastModels.value?.groupId === board.groupId ? lastModels.value.models : []
  return models.map((m) => ({ value: m, label: m }))
})
function pickModel(value: unknown) {
  const model = String(value ?? '').trim()
  if (model) setSchedBoardModel(model)
}

// The picked model's supply now; every count unknown when the backend could not read it, or when the board's latest
// refresh failed (the snapshot shown before is not now's, review item 3). The next answer brings a new snapshot.
const supplyText = computed(() => {
  const supply = failed.value ? null : data.value?.supply ?? null
  const count = (value: number | undefined) => (value == null ? t('admin.schedQuality.board.supplyUnknown') : String(value))
  return {
    counts: t('admin.schedQuality.board.supplyCounts', { declared: count(supply?.declared), enabled: count(supply?.enabled), available: count(supply?.available) }),
    at: supply ? t('admin.schedQuality.board.supplyAt', { time: formatShortTime(supply.at) }) : '',
    why: failed.value ? t('admin.schedQuality.board.supplyRefreshFailed') : t('admin.schedQuality.board.supplyFailed')
  }
})

// The summary line: a missing summary or field reads "—".
const summaryText = computed(() => {
  const summary = boardData.summary?.group_id === board.groupId ? boardData.summary : null
  const count = (value: number | null | undefined) => (value == null ? DASH : String(value))
  const wait = (value: number | null | undefined) => (value == null ? DASH : formatRecentSeconds(value))
  const accounts = summary?.accounts
  const last60 = summary?.last60
  return {
    counts: t('admin.schedQuality.board.summaryCounts', {
      pass: count(accounts?.pass), unknown: count(accounts?.unknown), trial: count(accounts?.trial), rear: count(accounts?.rear)
    }),
    wait: last60?.requests == null
      ? t('admin.schedQuality.board.summaryWait')
      : t('admin.schedQuality.board.summaryWaitRequests', { n: last60.requests }, last60.requests),
    e2e: t('admin.schedQuality.board.summaryFromGateway', { p50: wait(last60?.e2e_first_output_p50_ms), p90: wait(last60?.e2e_first_output_p90_ms) }),
    body: t('admin.schedQuality.board.summaryFromBody', { p50: wait(last60?.body_first_output_p50_ms), p90: wait(last60?.body_first_output_p90_ms) }),
    // No count, no unit: "—" alone.
    migration: last60?.migration_400 == null
      ? t('admin.schedQuality.board.summaryMigrationNone')
      : t('admin.schedQuality.board.summaryMigration', { n: last60.migration_400 }),
    // A summary whose 24-hour count was not read says so (review item 4); no summary at all reads "—" like the rest.
    rearEntries: !summary
      ? t('admin.schedQuality.board.rearEntriesNone')
      : summary.rear_entries_24h
        ? t('admin.schedQuality.board.rearEntries', { decisions: summary.rear_entries_24h.decisions, account_models: summary.rear_entries_24h.account_models })
        : t('admin.schedQuality.board.rearEntriesUnknown')
  }
})

const IDENTITY_SHAPES: Record<BackendIdentity, string> = {
  pass: 'sq-shape sq-shape--pass',
  rear: 'sq-shape sq-shape--fail',
  trial: 'sq-shape sq-shape--trial',
  unknown: 'sq-text-3'
}

const stopped = (row: BackendOrderRow) => row.layer === 'stopped'
/** Takes no part in the group: stopped, or outside the rate range (`excluded` is absent or null otherwise). */
const unused = (row: BackendOrderRow) => stopped(row) || !!row.excluded

function stateOf(row: BackendOrderRow): { text: string; cls: string } {
  const left = secondsUntil(row.until, now.value)
  const timed = (text: string) => (left ? `${text} ${fmtCountdown(left)}` : text)
  // The backend sends an excluded account as stopped with the reason (profit_floor | profit_threshold).
  if (row.excluded) return { text: t('admin.schedQuality.board.excluded', { reason: rejectName(row.excluded, t, te) }), cls: '' }
  if (stopped(row)) return { text: timed(rejectName('not_schedulable', t, te)), cls: '' }
  if (row.exempt) return { text: t('admin.schedQuality.state.exempted'), cls: 'sq-text' }
  const layer = word('layer', row.layer)
  if (row.layer === 'rear') return { text: timed(layer), cls: 'sq-cool' }
  if (row.layer === 'trial' || row.layer === 'trial_full') return { text: timed(layer), cls: 'sq-trial' }
  return { text: layer, cls: 'sq-text' }
}

// ---- Pinning: saved at once. The board reads the scheduler's snapshot, which follows a change a few seconds later,
// so the switch keeps the saved state until an answer agrees (not just the next one), for at most PIN_HOLD_MS; after
// that the backend's value stands (someone changed it back, or the board never shows it). A failure shows the
// backend's reason (not a member, not an OpenAI group) until the next try, or until the same PIN_HOLD_MS is over (the
// board has followed the backend by then).
const PIN_HOLD_MS = 30_000
// Each state belongs to a group and an account (an account can be in several groups). Opening another group or closing
// the board starts a new opening: an answer from an earlier one leaves nothing behind (the write itself went through).
const pinKey = (groupId: number | null, accountId: number) => `${groupId}:${accountId}`
const rowKey = (row: BackendOrderRow) => pinKey(board.groupId, row.account_id)
const pinBusy = reactive(new Set<string>())
const pinFailed = reactive(new Map<string, { message: string; at: number }>())
const pinShown = reactive(new Map<string, { pinned: boolean; at: number }>())
let opening = 0
const held = (key: string) => {
  const shown = pinShown.get(key)
  return shown && now.value - shown.at <= PIN_HOLD_MS ? shown : null
}
watch(() => boardData.data, (value) => {
  for (const row of value?.rows ?? []) {
    // The board agrees, or the hold is over: the backend's value stands.
    const key = pinKey(value!.group_id, row.account_id)
    const shown = held(key)
    if (!shown || shown.pinned === row.pinned) pinShown.delete(key)
  }
})
watch(() => board.groupId, () => {
  opening++
  pinShown.clear()
  pinFailed.clear()
})
const pinnedOf = (row: BackendOrderRow) => held(rowKey(row))?.pinned ?? row.pinned
const pinFailedOf = (row: BackendOrderRow) => {
  const failure = pinFailed.get(rowKey(row))
  return failure && now.value - failure.at <= PIN_HOLD_MS ? failure.message : null
}

async function setPinned(row: BackendOrderRow, pinned: boolean) {
  const groupId = board.groupId
  const key = rowKey(row)
  if (groupId === null || !canManageGroups.value || pinBusy.has(key)) return
  const asked = opening
  pinBusy.add(key)
  pinFailed.delete(key)
  try {
    const result = await adminAPI.groups.setMemberPinned(groupId, row.account_id, pinned)
    if (asked === opening) pinShown.set(key, { pinned: result.pinned, at: now.value })
    void markSchedChanged()
  } catch (error) {
    if (asked === opening) pinFailed.set(key, { message: extractApiErrorMessage(error, ''), at: now.value })
  } finally {
    pinBusy.delete(key)
  }
}
</script>
