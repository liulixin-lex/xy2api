<template>
  <section v-if="tasks.length || loading || error || busy" class="rounded-xl border border-gray-200 bg-white px-4 py-4 dark:border-dark-700 dark:bg-dark-800" :aria-label="t('usageExports.title')">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h3 class="font-medium text-gray-900 dark:text-gray-100">{{ t('usageExports.title') }}</h3>
        <p class="mt-1 text-sm text-gray-600 dark:text-gray-300">{{ t('usageExports.retention') }}</p>
      </div>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button>
    </div>
    <p v-if="waiting" class="mt-3 text-sm text-gray-700 dark:text-gray-200" role="status">{{ t('usageExports.waiting') }}</p>
    <p v-if="error" class="mt-3 text-sm text-red-700 dark:text-red-300" role="alert">{{ errorText(error) }}</p>
    <ul class="mt-3 divide-y divide-gray-100 dark:divide-dark-700">
      <li v-for="task in tasks" :key="task.id" class="flex flex-wrap items-center justify-between gap-3 py-3">
        <div class="min-w-0">
          <p class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ task.format.toUpperCase() }} · {{ date(task.created_at) }}</p>
          <p class="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {{ t(`usageExports.states.${task.status === 'running' ? task.phase : task.status}`) }}
            · {{ t('usageExports.rows', { count: (task.total_rows ?? task.processed_rows).toLocaleString() }) }}
          </p>
          <p v-if="task.snapshot_at" class="mt-1 text-sm text-gray-600 dark:text-gray-300">{{ t('usageExports.snapshot', { time: date(task.snapshot_at) }) }}</p>
          <p v-if="task.error_code" class="mt-1 max-w-prose text-sm text-red-700 dark:text-red-300">{{ errorText(task.error_code) }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button v-if="task.status === 'queued' || task.status === 'running'" type="button" class="btn btn-secondary" :disabled="!!pendingAction" @click="action(task, 'cancel')">{{ t('common.cancel') }}</button>
          <template v-else>
            <button v-if="task.status === 'succeeded'" type="button" class="btn btn-primary" :disabled="!!pendingAction" @click="action(task, 'download-ticket')">{{ t('usageExports.download') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="!!pendingAction" @click="action(task, 'delete')">{{ t('common.delete') }}</button>
          </template>
        </div>
      </li>
    </ul>
    <div v-if="total > 20" class="mt-2 flex items-center justify-end gap-3">
      <button class="btn btn-secondary" type="button" :disabled="page <= 1 || loading" @click="page--">{{ t('usageExports.previous') }}</button>
      <span class="text-sm tabular-nums">{{ page }} / {{ Math.ceil(total / 20) }}</span>
      <button class="btn btn-secondary" type="button" :disabled="page * 20 >= total || loading" @click="page++">{{ t('usageExports.next') }}</button>
    </div>
  </section>
</template>
<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ExportScope } from '@/api/usageExport'
import { useUsageExports } from '@/composables/useUsageExports'
const props = defineProps<{ scope: ExportScope }>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const { t, te, locale } = useI18n()
const { tasks, total, page, loading, busy, pendingAction, error, waiting, create, refresh, action } = useUsageExports(props.scope)
const date = (value: string) => new Date(value).toLocaleString(locale.value)
const errorText = (code: string) => te(`usageExports.errors.${code}`) ? t(`usageExports.errors.${code}`) : t('usageExports.errors.EXPORT_GENERATION_FAILED')
watch(busy, value => emit('busy', value), { immediate: true })
defineExpose({ create })
</script>
