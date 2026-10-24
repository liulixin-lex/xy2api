<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-6 pb-8">
      <!-- The app header shows the route title from lg up; this card carries it on smaller screens. -->
      <header class="rounded-3xl bg-white p-5 lg:hidden shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6">
        <h1 class="flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
          <span class="inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
            <Icon name="database" size="sm" aria-hidden="true" />
          </span>
          {{ t('admin.cacheObservability.title') }}
        </h1>
        <p class="mt-1.5 max-w-3xl text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheObservability.description') }}</p>
      </header>

      <!-- M1 ships only the switches; the observability charts join as a second tab once their endpoints exist (DESIGN §11.7). -->
      <CacheSettingsPanel />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import CacheSettingsPanel from './settings/CacheSettingsPanel.vue'

const { t } = useI18n()
</script>
