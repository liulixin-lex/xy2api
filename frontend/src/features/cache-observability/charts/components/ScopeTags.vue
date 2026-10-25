<template>
  <!-- Outlined like the risk tags: they qualify the numbers, they are not a state (M2b scope_notes). -->
  <span v-if="tags.length" class="inline-flex flex-wrap gap-1.5">
    <span
      v-for="tag in tags"
      :key="tag"
      class="rounded-md border border-gray-300 px-1.5 py-0.5 text-xs font-medium text-gray-700 dark:border-dark-500 dark:text-dark-200"
      :data-test="`scope-tag-${tag}`"
    >
      {{ t(`admin.cacheCharts.scopeTags.${tag}`) }}
    </span>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ tags: ('openaiOnly' | 'bridgeOnly')[] }>()
const { t } = useI18n()
</script>
