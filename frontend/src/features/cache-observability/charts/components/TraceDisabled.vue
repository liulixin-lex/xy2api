<template>
  <div class="rounded-xl bg-gray-50 px-4 py-4 text-sm text-gray-700 dark:bg-dark-900/60 dark:text-dark-200" data-test="trace-disabled">
    <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.cacheCharts.trace.disabledTitle') }}</p>
    <p class="mt-1 max-w-prose">{{ t('admin.cacheCharts.trace.disabledBody') }}</p>
    <button type="button" class="btn btn-secondary btn-sm mt-3" @click="emit('open-settings')">{{ t('admin.cacheCharts.trace.openSettings') }}</button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const emit = defineEmits<{ (e: 'open-settings'): void }>()
const { t } = useI18n()
</script>
