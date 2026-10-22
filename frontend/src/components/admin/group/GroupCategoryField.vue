<template>
  <div data-test="group-category-field">
    <label class="input-label">{{ t('admin.groupCategories.field') }}</label>
    <div v-if="store.categories.length" class="flex flex-wrap gap-2">
      <label
        v-for="category in store.categories"
        :key="category.id"
        :class="[
          'inline-flex max-w-full cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1 text-sm transition-colors',
          selected.includes(category.id)
            ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/20 dark:text-primary-300'
            : 'border-gray-200 text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700'
        ]"
      >
        <input
          v-model="selected"
          type="checkbox"
          :value="category.id"
          :data-category-id="category.id"
          class="h-3.5 w-3.5 shrink-0 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
        />
        <span class="truncate">{{ category.name }}</span>
      </label>
    </div>
    <p v-else class="text-sm text-gray-500 dark:text-gray-400">
      {{ store.loadState === 'failed' ? t('admin.groupCategories.loadFailed') : t('admin.groupCategories.fieldEmpty') }}
    </p>
    <p class="input-hint">{{ t('admin.groupCategories.fieldHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useGroupCategoriesStore } from '@/stores/groupCategories'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  /** Absent when creating a group. */
  groupId?: number
}>()

const { t } = useI18n()
const appStore = useAppStore()
const store = useGroupCategoriesStore()

const selected = ref<number[]>([])
let initial: number[] = []

const currentIds = (groupId: number | undefined) =>
  groupId ? store.categoriesForGroup(groupId).map((category) => category.id) : []

onMounted(async () => {
  await store.ensureLoaded()
  initial = currentIds(props.groupId)
  selected.value = [...initial]
})

const sameSet = (left: number[], right: number[]) =>
  left.length === right.length && left.every((id) => right.includes(id))

/**
 * 分组保存成功后调用；分类没变就不发请求。失败只提示“分组已保存，分类未保存”，
 * 返回 false，不抛错，免得调用方把分组当成没保存。
 */
async function save(groupId: number): Promise<boolean> {
  const next = store.categories.filter((category) => selected.value.includes(category.id)).map((category) => category.id)
  if (sameSet(next, initial)) return true
  try {
    await store.setGroupCategories(groupId, next)
    initial = next
    return true
  } catch (error) {
    appStore.showWarning(t('admin.groupCategories.groupSavedCategoriesFailed', { message: extractApiErrorMessage(error, '') }))
    return false
  }
}

defineExpose({ save })
</script>
