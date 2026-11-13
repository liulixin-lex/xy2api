<template>
  <div class="mb-4 flex flex-wrap items-center justify-between gap-2 rounded-lg bg-primary-50 p-3 dark:bg-primary-900/20">
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ allResultsSelected
          ? t('admin.accounts.bulkActions.selectedAll', { count: selectedIds.length })
          : t('admin.accounts.bulkActions.selected', { count: selectedIds.length }) }}
      </span>
      <button :class="LINK" @click="$emit('select-page')">{{ t('admin.accounts.bulkActions.selectCurrentPage') }}</button>
      <template v-if="!allResultsSelected && totalResults > selectedIds.length">
        <span class="text-gray-300 dark:text-primary-800">•</span>
        <button :class="LINK" class="disabled:cursor-not-allowed disabled:opacity-60" :disabled="selectingAll" @click="$emit('select-all-results')">
          {{ selectingAll ? t('admin.accounts.bulkActions.selectingAll') : t('admin.accounts.bulkActions.selectAllResults', { count: totalResults }) }}
        </button>
      </template>
      <span class="text-gray-300 dark:text-primary-800">•</span>
      <button :class="LINK" @click="$emit('clear')">{{ t('admin.accounts.bulkActions.clear') }}</button>
    </div>
    <div class="flex flex-wrap items-center justify-end gap-2" data-test="bulk-actions">
      <button
        v-for="action in SCHED_ACTIONS"
        :key="action"
        type="button"
        class="btn btn-secondary btn-sm"
        :data-test="`bulk-sched-${action}`"
        :disabled="schedBusy || (action === 'reset' && recoverBusy)"
        :aria-busy="schedBusy || (action === 'reset' && recoverBusy)"
        @click="$emit('sched-action', action, $event.currentTarget)"
      >{{ t(`admin.schedQuality.action.${action}`) }}</button>
      <span :class="SEP" aria-hidden="true" />
      <button type="button" class="btn btn-secondary btn-sm" :disabled="schedulingBusy" :aria-busy="schedulingBusy" @click="$emit('toggle-schedulable', true)">{{ t('admin.accounts.bulkActions.enableScheduling') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="schedulingBusy" :aria-busy="schedulingBusy" @click="$emit('toggle-schedulable', false)">{{ t('admin.accounts.bulkActions.disableScheduling') }}</button>
      <span :class="SEP" aria-hidden="true" />
      <button type="button" class="btn btn-primary btn-sm" @click="$emit('edit-selected')">{{ t('admin.accounts.bulkActions.edit') }}</button>
      <button
        ref="moreRef"
        type="button"
        class="btn btn-secondary btn-sm"
        aria-haspopup="menu"
        :aria-expanded="moreRect !== null"
        data-test="bulk-more"
        @click="toggleMore"
      >{{ t('common.more') }}<Icon name="chevronDown" size="xs" class="ml-1" /></button>
    </div>

    <Teleport to="body">
      <div v-if="moreRect" ref="menuRootRef">
        <div class="fixed inset-0" :style="{ zIndex: layer.zIndex.value }" @click="moreRect = null" />
        <div
          role="menu"
          class="fixed w-44 rounded-xl bg-white py-1 shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
          :style="{ top: `${moreRect.bottom + 4}px`, right: `${Math.max(8, viewportWidth - moreRect.right)}px`, zIndex: (layer.zIndex.value ?? 9998) + 1 }"
          data-test="bulk-more-menu"
        >
          <button type="button" role="menuitem" :class="ITEM" @click="pick('reset-status')">{{ t('admin.accounts.bulkActions.resetStatus') }}</button>
          <button type="button" role="menuitem" :class="ITEM" @click="pick('refresh-token')">{{ t('admin.accounts.bulkActions.refreshToken') }}</button>
          <button type="button" role="menuitem" :class="ITEM" @click="pick('probe-upstream-billing')">{{ t('admin.accounts.bulkActions.probeUpstreamBilling') }}</button>
          <button type="button" role="menuitem" :class="ITEM" :disabled="iqBusy" :aria-busy="iqBusy" data-testid="bulk-iq-check" @click="pick('run-iq-check')">{{ t('admin.accounts.bulkActions.iqCheck') }}</button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700" />
          <button type="button" role="menuitem" :class="ITEM" class="!text-red-600 dark:!text-red-400" @click="pick('delete')">{{ t('admin.accounts.bulkActions.delete') }}</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, shallowRef } from 'vue'
import { useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import { useOverlayLayer } from '@/composables/useOverlayLayer'

// Shown only while accounts are selected (design spec R2 6.1 #7): quality actions, scheduling, edit, and the rest
// under 更多.
defineProps<{
  selectedIds: number[]
  totalResults: number
  selectingAll: boolean
  allResultsSelected: boolean
  schedulingBusy?: boolean
  iqBusy?: boolean
  /** A quality batch is still running. */
  schedBusy?: boolean
  /** A batch recovery is running, or one of the selected accounts is being recovered. */
  recoverBusy?: boolean
}>()

type MoreAction = 'reset-status' | 'refresh-token' | 'probe-upstream-billing' | 'run-iq-check' | 'delete'
const emit = defineEmits([
  'delete',
  'edit-selected',
  'clear',
  'select-page',
  'select-all-results',
  'toggle-schedulable',
  'reset-status',
  'refresh-token',
  'probe-upstream-billing',
  'run-iq-check',
  'sched-action'
])

const SCHED_ACTIONS = ['reset', 'exempt'] as const
const LINK = 'text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200'
const SEP = 'mx-1 w-px self-stretch bg-gray-300 dark:bg-dark-600'
const ITEM = 'flex w-full items-center px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:text-gray-200 dark:hover:bg-dark-700'

const { t } = useI18n()
const { width: viewportWidth } = useWindowSize()
const moreRef = ref<HTMLElement | null>(null)
const menuRootRef = ref<HTMLElement | null>(null)
const moreRect = shallowRef<DOMRect | null>(null)

function toggleMore() {
  moreRect.value = moreRect.value ? null : (moreRef.value?.getBoundingClientRect() ?? null)
}

function pick(action: MoreAction) {
  moreRect.value = null
  emit(action)
}

// Escape closes the menu before the page below (design spec 0.2).
const layer = useOverlayLayer(() => moreRect.value !== null, { root: menuRootRef, modal: false, floor: 9998, onEscape: () => { moreRect.value = null } })
</script>
