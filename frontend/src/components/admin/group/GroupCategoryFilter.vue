<template>
  <Select
    v-if="visible"
    :model-value="modelValue"
    :options="options"
    :aria-label="t('admin.groupCategories.allCategories')"
    data-test="group-category-filter"
    @update:model-value="handleUpdate"
    @change="emit('change')"
  />
</template>

<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { useAuthStore } from '@/stores/auth'
import { useGroupCategoriesStore } from '@/stores/groupCategories'

const props = defineProps<{
  modelValue: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  change: []
}>()

// 分类筛选：有分类或已选中值时才显示；选中的分类被删后回到全部分类
const { t } = useI18n()
const authStore = useAuthStore()
const store = useGroupCategoriesStore()

const visible = computed(() => !authStore.isSimpleMode && (store.categories.length > 0 || props.modelValue !== ''))

const options = computed(() => [
  { value: '', label: t('admin.groupCategories.allCategories') },
  ...store.categories.map((category) => ({ value: String(category.id), label: category.name }))
])

const handleUpdate = (value: string | number | boolean | null) => {
  emit('update:modelValue', value === null ? '' : String(value))
}

// 页面级加载入口：每次进入页面都取一次最新分类
onMounted(() => {
  if (!authStore.isSimpleMode) void store.reload()
})

// 只在成功加载后复核；加载失败时保留当前值
watch(
  () => [store.loadState, store.categories, props.modelValue] as const,
  ([state, categories, value]) => {
    if (state !== 'ready' || value === '') return
    if (categories.some((category) => String(category.id) === value)) return
    emit('update:modelValue', '')
    emit('change')
  },
  { immediate: true }
)
</script>
