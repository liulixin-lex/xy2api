<template>
  <section
    class="flex min-w-0 flex-col rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6"
    :aria-labelledby="titleId"
    :aria-busy="status === 'loading' || refreshing || undefined"
    :data-test="`section-${id}`"
    :data-status="status"
  >
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h2 :id="titleId" class="text-sm font-bold text-gray-900 dark:text-white">{{ title }}</h2>
        <p v-if="subtitle" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ subtitle }}</p>
      </div>
      <slot name="actions" />
    </header>

    <div v-if="status === 'loading'" role="status" :aria-label="t('admin.cacheCharts.state.loading')" class="space-y-3">
      <div v-for="n in skeletonRows" :key="n" class="h-6 animate-pulse rounded bg-gray-100 motion-reduce:animate-none dark:bg-dark-700" :style="{ width: `${100 - ((n * 13) % 40)}%` }" />
    </div>
    <div v-else-if="status === 'error'" role="alert" class="flex flex-wrap items-center gap-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200">
      <p class="min-w-0 flex-1">{{ error ? t('admin.cacheCharts.state.errorWith', { message: error }) : t('admin.cacheCharts.state.error') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="emit('retry')">{{ t('admin.cacheCharts.state.retry') }}</button>
    </div>
    <p v-else-if="status === 'not_ready'" class="rounded-xl bg-gray-50 px-4 py-3 text-sm text-gray-700 dark:bg-dark-900/60 dark:text-dark-200">
      {{ t('admin.cacheCharts.state.notReady') }}
    </p>
    <p v-else-if="status === 'unavailable'" class="rounded-xl bg-gray-50 px-4 py-3 text-sm text-gray-700 dark:bg-dark-900/60 dark:text-dark-200">
      {{ t('admin.cacheCharts.state.unavailable') }}
    </p>
    <div v-else-if="status === 'ready'" class="min-w-0 flex-1 transition-opacity duration-150" :class="refreshing ? 'opacity-60' : ''">
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SectionStatus } from '../useCacheCharts'

const props = withDefaults(
  defineProps<{ id: string; title: string; subtitle?: string; status: SectionStatus; refreshing?: boolean; error?: string | null; skeletonRows?: number }>(),
  { subtitle: '', refreshing: false, error: null, skeletonRows: 4 },
)
const emit = defineEmits<{ (e: 'retry'): void }>()
const { t } = useI18n()
const titleId = computed(() => `cache-charts-${props.id}`)
</script>
