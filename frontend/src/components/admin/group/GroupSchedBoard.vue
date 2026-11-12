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
        class="w-[150px] text-xs"
        @update:model-value="(value) => (requested = String(value))"
      />
    </template>

    <div v-if="!data && !poll.error.value" data-sq-board-loading>
      <span v-for="i in 6" :key="i" :class="['skeleton block h-3.5 w-full', i === 1 ? '' : 'mt-4']" />
    </div>
    <div v-else-if="!data" class="flex items-center gap-3" data-sq-board-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="poll.refresh()">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <p v-else-if="!data.accounts.length" class="sq-text-3 text-sm">{{ t('admin.schedQuality.state.nodata') }}</p>
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
            v-for="(row, index) in data.accounts"
            :key="row.account_id"
            :class="{ 'sq-weak': !row.eligible, 'sq-split': !row.eligible && index > 0 && data.accounts[index - 1].eligible }"
            :data-sq-board-row="row.account_id"
          >
            <td class="sq-r sq-num sq-text-3">{{ row.eligible ? row.rank : '—' }}</td>
            <td>
              <button type="button" class="sq-link sq-focus" :data-sq-trigger="`account:${row.account_id}`" @click="openSchedDrawer({ id: row.account_id, name: row.name }, { model: data.model })">{{ row.name }}</button>
            </td>
            <td>
              <span class="inline-flex items-center gap-1.5">
                <span :class="stateOf(row).cls" data-sq-state>{{ stateOf(row).text }}</span>
                <span
                  v-if="noTakeover(row)"
                  v-tip="t('admin.schedQuality.tip.noTakeover')"
                  role="img"
                  tabindex="0"
                  :aria-label="t('admin.schedQuality.tip.noTakeover')"
                  class="sq-icon-mark sq-focus inline-flex"
                  data-sq-no-takeover
                >
                  <svg viewBox="0 0 14 14" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M2.5 4.5h7.5M8 2.5l2 2-2 2M11.5 9.5H4M6 7.5l-2 2 2 2M2 12L12 2" />
                  </svg>
                </span>
              </span>
            </td>
            <td class="sq-r sq-num">{{ row.priority }}</td>
            <td class="sq-r sq-num">{{ row.rate.toFixed(3) }}</td>
            <td class="sq-r sq-num">
              <span class="inline-flex items-center gap-1.5">
                <span
                  v-if="row.recently_full_seconds > 0"
                  v-tip="t('admin.schedQuality.tip.recentlyFull')"
                  role="img"
                  tabindex="0"
                  :aria-label="t('admin.schedQuality.tip.recentlyFull')"
                  class="sq-full sq-focus"
                />
                {{ row.in_flight }} / {{ row.concurrency }}
              </span>
            </td>
            <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num">
              <span
                v-tip="t('admin.schedQuality.tip.samples', { n: row[dim].n })"
                tabindex="0"
                :aria-label="valueLabel(row, dim)"
                :class="['sq-focus', valueClass(row, dim)]"
              >{{ valueText(row, dim) }}</span>
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
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedBoardAccount, SchedTakeoverIdleSeconds } from '@/types/schedQuality'
import { secondsUntil, useSchedQualityClock, useSchedQualityPoll } from '@/composables/useSchedQuality'
import { closeSchedBoard, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import { DASH, fmtCacheRatio, fmtCountdown, fmtErrorRate, fmtTtfo, rejectName, SCHED_DIMS, verdictClass, type SchedDim } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// Group board (design spec C): the group's accounts in the API's selection order (never re-sorted here), refreshed
// every 5 s while open. Accounts held back by hard admission come last in the secondary colour with their reason.
const { t, te } = useI18n()
const { board, changed } = useSchedLayers()
const now = useSchedQualityClock()
const shellRef = ref<{ holdFocus: () => () => void } | null>(null)

// The model asked for: the opener's, then whatever is picked here; the API answers with the model it used.
const requested = ref<string | null>(null)
watch(() => [board.groupId, board.raise] as const, () => { requested.value = board.model }, { immediate: true })

const poll = useSchedQualityPoll(
  () => (board.groupId === null ? null : `${board.groupId}:${requested.value ?? ''}`),
  (signal) => schedQualityAPI.groupBoard(board.groupId!, requested.value ?? undefined, { signal })
)
const data = computed(() => poll.data.value)
watch(() => board.raise, () => { if (board.groupId !== null) void poll.refresh() })
watch(changed, () => { if (board.groupId !== null) void poll.refresh() })

let restoreFocus: (() => void) | null = null
watch(() => poll.data.value, () => { restoreFocus = shellRef.value?.holdFocus() ?? null }, { flush: 'pre' })
watch(() => poll.data.value, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

const modelOptions = computed(() => (data.value?.models ?? []).map((m) => ({ value: m, label: m })))

// Takeover setting of the group, for the "no takeover" mark.
const takeover = ref<SchedTakeoverIdleSeconds | null>(null)
watch(() => board.groupId, async (id) => {
  takeover.value = null
  if (id === null) return
  try {
    const policy = await schedQualityAPI.getGroupPolicy(id)
    if (board.groupId === id) takeover.value = policy.policy.takeover_idle_seconds
  } catch {
    // Without the policy the mark stays hidden.
  }
}, { immediate: true })

function stateOf(row: SchedBoardAccount): { text: string; cls: string } {
  if (!row.eligible) {
    const word = rejectName(row.reject, t, te)
    const left = secondsUntil(row.reject_until, now.value)
    return { text: left ? `${word} ${fmtCountdown(left)}` : word, cls: '' }
  }
  if (row.override?.mode === 'demote') return { text: t('admin.schedQuality.state.demoted'), cls: 'sq-text' }
  if (row.override?.mode === 'exempt') return { text: t('admin.schedQuality.state.exempted'), cls: 'sq-text' }
  if (row.state === 'cooling') {
    if (row.testing) return { text: t('admin.schedQuality.state.testing'), cls: 'sq-test' }
    const left = secondsUntil(row.until, now.value)
    const word = t('admin.schedQuality.state.cooling')
    return { text: !row.waiting_test && left ? `${word} ${fmtCountdown(left)}` : word, cls: 'sq-cool' }
  }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}

function noTakeover(row: SchedBoardAccount) {
  const value = data.value
  return !!value && row.eligible && row.standing === 'front' && !row.can_take_over &&
    takeover.value !== null && takeover.value >= 0 && value.global_enabled && !value.stale
}

function valueText(row: SchedBoardAccount, dim: SchedDim) {
  if (dim === 'ttfo') return fmtTtfo(row.ttfo.p90_ms, row.ttfo.p90_timeout, t)
  return dim === 'error' ? fmtErrorRate(row.error.rate) : fmtCacheRatio(row.cache.ratio)
}

function valueClass(row: SchedBoardAccount, dim: SchedDim) {
  if (valueText(row, dim) === DASH) return 'sq-text-3'
  if (!row.eligible) return ''
  const verdict = row.verdicts[dim]
  return verdict === 'nodata' ? 'sq-text' : verdictClass(verdict)
}

function valueLabel(row: SchedBoardAccount, dim: SchedDim) {
  const name = t(`admin.schedQuality.dim.${dim}`)
  const text = valueText(row, dim)
  const verdict = row.verdicts[dim]
  if (text === DASH) return `${name} ${t('admin.schedQuality.aria.nodata')}`
  if (!row.eligible || verdict === 'nodata') return `${name} ${text}`
  return t('admin.schedQuality.aria.cell', { dim: name, value: text, verdict: t(`admin.schedQuality.aria.${verdict}`) })
}
</script>
