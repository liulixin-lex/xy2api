<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-6 pb-8">
      <!-- The app header shows the route title from lg up; this card carries it on smaller screens. -->
      <header class="rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6 lg:hidden">
        <h1 class="flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
          <span class="inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
            <Icon name="database" size="sm" aria-hidden="true" />
          </span>
          {{ t('admin.cacheObservability.title') }}
        </h1>
        <p class="mt-1.5 max-w-3xl text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheObservability.description') }}</p>
      </header>

      <!-- Charts depend on the P0b endpoints; without them (404) the page shows only the switches (DESIGN §11.7). -->
      <div v-if="charts.available.value" v-roving-tabs class="tabs inline-flex w-full max-w-xl sm:w-auto" role="tablist" :aria-label="t('admin.cacheObservability.tabsAria')">
        <button
          v-for="tab in tabs"
          :id="`cache-tab-${tab}`"
          :key="tab"
          type="button"
          role="tab"
          class="tab flex-1 sm:flex-none"
          :class="activeTab === tab ? 'tab-active' : ''"
          :aria-selected="activeTab === tab"
          :aria-controls="`cache-panel-${tab}`"
          :data-test="`tab-${tab}`"
          @click="selectTab(tab)"
        >
          {{ t(`admin.cacheObservability.tabs.${tab}`) }}
        </button>
      </div>

      <div
        v-if="charts.available.value !== false"
        id="cache-panel-patrol"
        v-show="activeTab === 'patrol'"
        role="tabpanel"
        aria-labelledby="cache-tab-patrol"
        data-test="panel-patrol"
      >
        <ChartsPanel :state="charts" @open-settings="selectTab('settings')" />
      </div>
      <!-- Without the chart endpoints there are no tabs, so the page still names the section (m-7). -->
      <h2 v-if="charts.available.value === false" class="text-lg font-bold text-gray-900 dark:text-white" data-test="settings-heading">
        {{ t('admin.cacheObservability.tabs.settings') }}
      </h2>
      <div
        id="cache-panel-settings"
        v-show="activeTab === 'settings'"
        :role="charts.available.value ? 'tabpanel' : undefined"
        :aria-labelledby="charts.available.value ? 'cache-tab-settings' : undefined"
        data-test="panel-settings"
      >
        <CacheSettingsPanel />
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { vRovingTabs } from '@/utils/rovingTabs'
import CacheSettingsPanel from './settings/CacheSettingsPanel.vue'
import ChartsPanel from './charts/ChartsPanel.vue'
import { useCacheCharts } from './charts/useCacheCharts'

type Tab = 'patrol' | 'settings'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const charts = useCacheCharts()
const tabs: Tab[] = ['patrol', 'settings']

const activeTab = computed<Tab>(() => {
  if (charts.available.value === false) return 'settings'
  const requested = route.query.tab
  if (requested === 'patrol' || requested === 'settings') return requested
  return 'patrol'
})

function selectTab(tab: Tab) {
  if (tab === activeTab.value) return
  void router.replace({ query: { ...route.query, tab } })
}

onMounted(() => {
  void charts.loadAll()
})
</script>
