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
    <p v-else-if="!data.rows.length" class="sq-text-3 text-sm">—</p>
    <div v-else class="-mx-3 overflow-x-auto">
      <table class="sq-table" data-sq-board-table>
        <thead>
          <tr>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.rank') }}</th>
            <th scope="col">{{ t('admin.schedQuality.col.account') }}</th>
            <th scope="col">{{ t('admin.schedQuality.col.state') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.drawer.priority') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.drawer.rate') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.load') }}</th>
            <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.lastUsed') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, index) in data.rows"
            :key="row.account_id"
            :class="{ 'sq-weak': row.layer === 'stopped', 'sq-split': index > 0 && data.rows[index - 1].layer !== row.layer }"
            :data-sq-board-row="row.account_id"
            :data-sq-layer="row.layer"
          >
            <td class="sq-r sq-num sq-text-3">{{ row.layer === 'stopped' ? '—' : row.rank }}</td>
            <td>
              <span class="inline-flex items-center gap-1.5">
                <button
                  v-if="canViewAccounts"
                  type="button"
                  class="sq-link sq-focus"
                  :data-sq-trigger="`account:${row.account_id}`"
                  @click="openSchedDrawer({ id: row.account_id, name: row.name }, { groupId: board.groupId, model: data.model })"
                >{{ row.name }}</button>
                <template v-else>{{ row.name }}</template>
                <SchedOverrideCapsule v-if="row.exempt" />
              </span>
            </td>
            <td><span :class="stateOf(row).cls" data-sq-state>{{ stateOf(row).text }}</span></td>
            <td class="sq-r sq-num">{{ row.priority }}</td>
            <td class="sq-r sq-num">{{ row.rate.toFixed(3) }}</td>
            <td class="sq-r sq-num">{{ row.in_flight }} / {{ row.concurrency }}</td>
            <td class="sq-r sq-num sq-text-3">{{ row.last_used_at ? fmtAt(row.last_used_at, now) : '—' }}</td>
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
import SchedOverrideCapsule from '@/components/admin/account/quality/SchedOverrideCapsule.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedOrderRow } from '@/types/schedQuality'
import { useSchedQualityClock, useSchedQualityPoll } from '@/composables/useSchedQuality'
import { closeSchedBoard, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { fmtAt } from '@/utils/schedQualityFormat'

// Selection order of this moment (FINAL §1, §7; contract §5.7): the group's accounts for one model in the order the
// server would pick them for a new session, never re-sorted here — normal first, then substandard, then the ones that
// cannot take requests. Each row shows the keys of the order: priority, rate, load, last use. Refreshed every 5 s.
const { t } = useI18n()
const { board, changed } = useSchedLayers()
// The account link needs accounts:view; the name is plain text otherwise.
const { canView: canViewAccounts } = useAdminPermission('accounts')
const now = useSchedQualityClock()
const shellRef = ref<{ holdFocus: () => () => void } | null>(null)

// The model asked for: the opener's, then whatever is picked here; the API answers with the model it used.
const requested = ref<string | null>(null)
watch(() => [board.groupId, board.raise] as const, () => { requested.value = board.model }, { immediate: true })

const poll = useSchedQualityPoll(
  () => (board.groupId === null ? null : `${board.groupId}:${requested.value ?? ''}`),
  (signal) => schedQualityAPI.groupOrder(board.groupId!, requested.value ?? undefined, { signal })
)
const data = computed(() => poll.data.value)
watch(() => board.raise, () => { if (board.groupId !== null) void poll.refresh() })
watch(changed, () => { if (board.groupId !== null) void poll.refresh() })

let restoreFocus: (() => void) | null = null
watch(() => poll.data.value, () => { restoreFocus = shellRef.value?.holdFocus() ?? null }, { flush: 'pre' })
watch(() => poll.data.value, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

const modelOptions = computed(() => (data.value?.models ?? []).map((m) => ({ value: m, label: m })))

function stateOf(row: SchedOrderRow): { text: string; cls: string } {
  if (row.layer === 'stopped') return { text: t('admin.schedQuality.state.disabled'), cls: 'sq-text-3' }
  if (row.layer === 'below') {
    const dims = row.dims.map((dim) => t(`admin.schedQuality.dim.${dim}`))
    const word = [t('admin.schedQuality.state.substandard'), ...dims].join('·')
    return { text: row.until ? `${word} · ${fmtAt(row.until, now.value)}` : word, cls: 'sq-cool' }
  }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}
</script>
