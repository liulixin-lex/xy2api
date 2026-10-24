<template>
  <div
    :id="id"
    ref="root"
    tabindex="-1"
    role="region"
    :aria-label="t('admin.cacheSettings.history.toggle')"
    class="mt-3 rounded-2xl bg-gray-50 p-4 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:bg-dark-900/60"
    data-test="policy-history"
  >
    <p v-if="!history || history === 'loading'" class="text-sm text-gray-600 dark:text-dark-300" role="status">{{ t('admin.cacheSettings.history.loading') }}</p>
    <div v-else-if="history === 'failed'" class="flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.cacheSettings.history.failed') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="reload">{{ t('admin.cacheSettings.retry') }}</button>
    </div>
    <template v-else>
      <p v-if="!history.items.length" class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.history.empty') }}</p>
      <ol v-else class="space-y-2" role="list">
        <li
          v-for="(item, index) in history.items"
          :key="`${item.at}-${index}`"
          class="grid gap-x-4 gap-y-0.5 text-sm text-gray-800 dark:text-gray-100 sm:grid-cols-[19rem_minmax(0,1fr)]"
          :data-failed="item.status_code >= 400 || undefined"
        >
          <time :datetime="item.at" class="tabular-nums text-gray-600 dark:text-dark-300">{{ formatBoth(item.at, t) }}</time>
          <div class="min-w-0">
            <p class="break-words">
              {{ item.actor?.email || t('admin.cacheSettings.footer.unknownActor') }}
              <span v-if="item.rev_after !== null" class="tabular-nums">{{ t('admin.cacheSettings.history.wrote', { rev: item.rev_after }) }}</span>
              <!-- Failed writes matter most when tracing an incident: marked and explained, not dimmed (m-5). -->
              <span v-if="item.status_code >= 400" class="badge badge-danger ml-1 align-middle">{{ failureText(item.status_code) }}</span>
            </p>
            <p v-if="item.changed_fields.length" class="break-words text-xs text-gray-600 dark:text-dark-300" :title="item.changed_fields.join(', ')">
              {{ t('admin.cacheSettings.history.changed', { fields: item.changed_fields.map(fieldLabel).join(t('admin.cacheSettings.nameSeparator')) }) }}
            </p>
            <p v-if="item.reason" class="break-words text-xs">{{ t('admin.cacheSettings.footer.reason') }}{{ item.reason }}</p>
          </div>
        </li>
      </ol>
      <!-- The reason says whether older entries are gone for good or a gap may still fill in (§7.6). -->
      <div v-if="!history.history_complete" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-600 dark:text-dark-300" data-test="history-incomplete">
        <p>{{ t(`admin.cacheSettings.history.incomplete.${incompleteReason(history)}`) }}</p>
        <button v-if="incompleteReason(history) === 'gap'" type="button" class="btn btn-ghost btn-sm" data-test="history-refresh" @click="reload">
          {{ t('admin.cacheSettings.history.refresh') }}
        </button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { fieldCopyKey } from '../catalog'
import { formatBoth } from '../format'
import type { HistoryIncompleteReason, HistoryResponse } from '../types'

const props = defineProps<{ id: string; policyKey: string; history: HistoryResponse | 'loading' | 'failed' | undefined }>()
const emit = defineEmits<{ (e: 'retry'): void }>()
const root = ref<HTMLElement | null>(null)

/** Reloading swaps the list, and the button with it, for a loading line; keep focus in the change log, not on <body>. */
function reload() {
  root.value?.focus()
  emit('retry')
}

const { t, te } = useI18n()

/** The audit log stores field paths; show the same names the card uses. */
function fieldLabel(path: string): string {
  const key = fieldCopyKey(props.policyKey, path, 'label')
  return te(key) ? t(key) : path
}

/** A backend before v1.7.4.6 sends no reason; its only cause was the retention period. */
function incompleteReason(history: HistoryResponse): HistoryIncompleteReason {
  return history.history_incomplete_reason === 'gap' ? 'gap' : 'earlier_missing'
}

function failureText(status: number): string {
  const key = `admin.cacheSettings.history.status.${status}`
  return t('admin.cacheSettings.history.failedWrite', { status, what: te(key) ? t(key) : t('admin.cacheSettings.history.status.other') })
}
</script>
