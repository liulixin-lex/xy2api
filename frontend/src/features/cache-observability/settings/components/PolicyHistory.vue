<template>
  <div :id="id" class="mt-3 rounded-2xl bg-gray-50 p-4 dark:bg-dark-900/60" data-test="policy-history">
    <p v-if="!history || history === 'loading'" class="text-sm text-gray-600 dark:text-dark-300" role="status">{{ t('admin.cacheSettings.history.loading') }}</p>
    <div v-else-if="history === 'failed'" class="flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.cacheSettings.history.failed') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="emit('retry')">{{ t('admin.cacheSettings.retry') }}</button>
    </div>
    <template v-else>
      <p v-if="!history.items.length" class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.history.empty') }}</p>
      <ol v-else class="space-y-2" role="list">
        <li
          v-for="(item, index) in history.items"
          :key="`${item.at}-${index}`"
          class="grid gap-x-4 gap-y-0.5 text-sm sm:grid-cols-[13rem_minmax(0,1fr)]"
          :class="item.status_code >= 400 ? 'text-gray-400 dark:text-dark-500' : 'text-gray-800 dark:text-gray-100'"
          :data-failed="item.status_code >= 400 || undefined"
        >
          <time :datetime="item.at" :title="formatUtc(item.at)" class="whitespace-nowrap tabular-nums text-gray-600 dark:text-dark-300">{{ formatUtc(item.at) }}</time>
          <div class="min-w-0">
            <p class="break-words">
              {{ item.actor?.email || t('admin.cacheSettings.footer.unknownActor') }}
              <span v-if="item.rev_before !== null && item.rev_after !== null" class="tabular-nums">{{ t('admin.cacheSettings.history.revChange', { from: item.rev_before, to: item.rev_after }) }}</span>
              <span v-if="item.status_code >= 400">{{ t('admin.cacheSettings.history.failedWrite', { status: item.status_code }) }}</span>
            </p>
            <p v-if="item.changed_fields.length" class="break-all font-mono text-xs text-gray-500 dark:text-dark-400">{{ item.changed_fields.join(', ') }}</p>
            <p v-if="item.reason" class="break-words text-xs">{{ t('admin.cacheSettings.footer.reason') }}{{ item.reason }}</p>
          </div>
        </li>
      </ol>
      <p v-if="!history.history_complete" class="mt-3 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.history.incomplete') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatUtc } from '../format'
import type { HistoryResponse } from '../types'

defineProps<{ id: string; history: HistoryResponse | 'loading' | 'failed' | undefined; now: Date }>()
const emit = defineEmits<{ (e: 'retry'): void }>()
const { t } = useI18n()
</script>
