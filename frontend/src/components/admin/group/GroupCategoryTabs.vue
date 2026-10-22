<template>
  <div
    v-roving-tabs.manual
    role="tablist"
    :aria-label="t('admin.groupCategories.tabsLabel')"
    data-test="group-category-tabs"
    class="flex max-w-[60%] shrink-0 items-center gap-1 overflow-x-auto"
  >
    <button
      v-for="tab in tabs"
      :key="tab.id"
      type="button"
      role="tab"
      :aria-selected="tab.id === activeId"
      :title="tab.name"
      :class="[
        'shrink-0 whitespace-nowrap rounded-md px-2 py-0.5 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40',
        tab.id === activeId
          ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
          : 'text-gray-600 hover:bg-white dark:text-gray-300 dark:hover:bg-dark-700'
      ]"
      @click="emit('toggle', tab.id)"
    >
      {{ tab.name }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { vRovingTabs } from '@/utils/rovingTabs'

defineProps<{
  tabs: { id: number; name: string }[]
  activeId: number | null
}>()

const emit = defineEmits<{
  toggle: [id: number]
}>()

// 分类标签与搜索框同一行；点当前标签再点一次恢复全部
const { t } = useI18n()
</script>
