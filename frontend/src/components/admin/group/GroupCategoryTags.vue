<template>
  <div v-if="names.length" class="flex max-w-48 flex-wrap gap-1" data-test="group-category-tags">
    <span
      v-for="name in names"
      :key="name"
      class="inline-flex max-w-full truncate rounded-md bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-600 dark:text-gray-300"
      :title="name"
    >
      {{ name }}
    </span>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useGroupCategoriesStore } from '@/stores/groupCategories'

const props = defineProps<{
  groupId: number
}>()

const store = useGroupCategoriesStore()

const names = computed(() => store.categoriesForGroup(props.groupId).map((category) => category.name))

onMounted(() => {
  void store.ensureLoaded()
})
</script>
