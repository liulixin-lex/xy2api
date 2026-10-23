<template>
  <section
    class="rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6"
    aria-labelledby="cache-existing-title"
    data-test="existing-mechanisms"
  >
    <h2 id="cache-existing-title" class="flex flex-wrap items-center gap-2 text-base font-bold text-gray-900 dark:text-white">
      {{ t('admin.cacheSettings.existing.title') }}
      <span class="badge badge-gray">{{ t('admin.cacheSettings.existing.readOnly') }}</span>
    </h2>
    <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.existing.description') }}</p>
    <dl class="mt-4 divide-y divide-gray-100 text-sm dark:divide-dark-700">
      <div class="flex flex-wrap items-center justify-between gap-3 py-3">
        <dt class="min-w-0">
          <span class="font-medium text-gray-900 dark:text-white">{{ t('admin.cacheSettings.existing.claudeFallback') }}</span>
          <code class="ml-2 break-all text-xs text-gray-500 dark:text-dark-400">claude_cache_fallback_policy</code>
        </dt>
        <dd class="flex items-center gap-3 text-gray-700 dark:text-dark-200">
          <span>{{ claudeText }}</span>
          <router-link :to="claudeLink" class="text-primary-700 underline-offset-2 hover:underline dark:text-primary-300">{{ t('admin.cacheSettings.existing.editInSettings') }}</router-link>
        </dd>
      </div>
      <div v-for="row in staticRows" :key="row.name" class="flex flex-wrap items-center justify-between gap-3 py-3">
        <dt class="min-w-0">
          <span class="font-medium text-gray-900 dark:text-white">{{ t(`admin.cacheSettings.existing.${row.label}`) }}</span>
          <code class="ml-2 break-all text-xs text-gray-500 dark:text-dark-400">{{ row.name }}</code>
        </dt>
        <dd class="flex items-center gap-3 text-gray-700 dark:text-dark-200">
          <span class="tabular-nums">{{ row.value }}</span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.existing.staticNote') }}</span>
        </dd>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-3 py-3">
        <dt class="font-medium text-gray-900 dark:text-white">{{ t('admin.cacheSettings.existing.resilience') }}</dt>
        <dd>
          <router-link to="/admin/settings" class="text-primary-700 underline-offset-2 hover:underline dark:text-primary-300">{{ t('admin.cacheSettings.existing.editInSettings') }}</router-link>
        </dd>
      </div>
    </dl>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ExistingMechanisms } from '../types'

const props = defineProps<{ existing: ExistingMechanisms | undefined }>()
const { t } = useI18n()

function onOff(value: boolean | null | undefined): string {
  if (value === null || value === undefined) return '-'
  return value ? t('admin.cacheSettings.values.on') : t('admin.cacheSettings.values.off')
}

const claude = computed(() => props.existing?.claude_cache_fallback ?? null)
const claudeText = computed(() => {
  if (!claude.value) return '-'
  return claude.value.configured
    ? t('admin.cacheSettings.existing.claudeConfigured', { n: claude.value.group_count })
    : t('admin.cacheSettings.values.off')
})
// Only an in-app path is followed; anything else falls back to the settings page.
const claudeLink = computed(() => {
  const link = claude.value?.settings_link
  return typeof link === 'string' && link.startsWith('/admin/') ? link : '/admin/settings'
})
const staticRows = computed(() => [
  { name: 'gateway.openai_cache.diagnostics_enabled', label: 'diagnostics', value: onOff(props.existing?.openai_cache_diagnostics_enabled) },
  { name: 'gateway.openai_cache.aware_routing_enabled', label: 'awareRouting', value: onOff(props.existing?.openai_cache_aware_routing_enabled) },
  {
    name: 'gateway.openai_cache.rollout_percent',
    label: 'awareRollout',
    value: typeof props.existing?.openai_cache_rollout_percent === 'number' ? `${props.existing.openai_cache_rollout_percent}%` : '-',
  },
])
</script>
