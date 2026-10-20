<template>
  <div>
    <VueDraggable
      :model-value="localColumns"
      :animation="150"
      handle=".column-drag-handle"
      class="grid grid-cols-1 gap-1"
      @update:model-value="onReorder"
    >
      <div
        v-for="col in localColumns"
        :key="col.key"
        data-test="column-setting"
        class="flex items-center rounded-md transition-colors hover:bg-gray-100 dark:hover:bg-dark-700"
      >
        <span
          class="column-drag-handle flex shrink-0 cursor-grab items-center py-2 pl-2 pr-1 text-gray-300 hover:text-gray-500 active:cursor-grabbing dark:text-dark-500 dark:hover:text-dark-300"
          :title="t('admin.accounts.dragToReorderColumns')"
        >
          <svg class="h-4 w-4" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
            <path d="M7 2a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM13 2a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM7 8a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM13 8a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM7 14a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM13 14a2 2 0 1 0 0 4 2 2 0 0 0 0-4z" />
          </svg>
        </span>
        <button
          type="button"
          class="flex min-w-0 flex-1 items-center justify-between py-2 pl-1 pr-3 text-sm text-gray-700 dark:text-gray-200"
          @click="emit('toggle', col.key)"
        >
          <span class="truncate">{{ col.label }}</span>
          <Icon v-if="isVisible(col.key)" name="check" size="sm" class="text-primary-500" />
        </button>
      </div>
    </VueDraggable>
    <button
      v-if="customized"
      type="button"
      data-test="reset-column-order"
      class="mt-1 w-full rounded-md px-3 py-2 text-left text-xs text-primary-600 transition-colors hover:bg-gray-100 dark:text-primary-400 dark:hover:bg-dark-700"
      @click="emit('reset')"
    >
      {{ t('admin.accounts.resetColumnOrder') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import Icon from '@/components/icons/Icon.vue'

interface ColumnSetting {
  key: string
  label: string
}

const props = defineProps<{
  columns: ColumnSetting[]
  isVisible: (key: string) => boolean
  customized?: boolean
}>()

const emit = defineEmits<{
  toggle: [key: string]
  reorder: [keys: string[]]
  reset: []
}>()

const { t } = useI18n()

// VueDraggable reorders a list it owns; mirror the parent's ordered columns into it.
const localColumns = ref<ColumnSetting[]>([...props.columns])
watch(
  () => props.columns,
  (columns) => {
    localColumns.value = [...columns]
  }
)

const onReorder = (columns: ColumnSetting[]) => {
  localColumns.value = columns
  emit('reorder', columns.map((col) => col.key))
}
</script>
