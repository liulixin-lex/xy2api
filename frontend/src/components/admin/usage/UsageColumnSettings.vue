<template>
  <!-- From 768px: the button and dropdown the usage pages had, plus "restore default columns". -->
  <div v-if="variant === 'menu'" ref="menuRef" class="relative">
    <button
      type="button"
      data-testid="usage-column-settings"
      class="btn btn-secondary px-2 md:px-3"
      :title="t('admin.users.columnSettings')"
      :aria-expanded="open"
      @click="open = !open"
    >
      <slot name="icon"><Icon name="grid" size="sm" /></slot>
      <span class="hidden md:inline">{{ t('admin.users.columnSettings') }}</span>
    </button>
    <div
      v-if="open"
      class="absolute right-0 top-full z-50 mt-1 max-h-80 w-48 overflow-y-auto rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
    >
      <button
        v-for="col in columns"
        :key="col.key"
        type="button"
        :data-testid="`usage-column-toggle-${col.key}`"
        class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
        @click="emit('toggle', col.key)"
      >
        <span>{{ col.label }}</span>
        <Icon v-if="isVisible(col.key)" name="check" size="sm" class="text-primary-500" :stroke-width="2" />
      </button>
      <button
        type="button"
        data-testid="usage-column-reset"
        class="mt-1 w-full border-t border-gray-100 px-4 py-2 text-left text-sm text-gray-600 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-700 dark:text-gray-400 dark:hover:bg-dark-700"
        :disabled="atDefault"
        @click="emit('reset')"
      >{{ t('common.table.resetColumns') }}</button>
    </div>
  </div>

  <!-- Below 768px: a square tool in the phone toolbar's info row, the list in a bottom sheet. -->
  <template v-else>
    <button
      type="button"
      data-testid="usage-column-settings"
      class="table-toolbar__icon-btn table-toolbar__icon"
      aria-haspopup="dialog"
      :aria-label="t('common.table.columnsTitle')"
      @click="open = true"
    >
      <Icon name="grid" size="sm" />
    </button>
    <BottomSheet :show="open" :title="t('common.table.columnsTitle')" test-id="usage-column-sheet" @close="open = false">
      <div class="-mx-1 grid">
        <label
          v-for="col in columns"
          :key="col.key"
          class="flex min-h-11 cursor-pointer items-center justify-between gap-4 rounded-lg px-1 text-sm text-gray-800 dark:text-gray-200"
        >
          <span>{{ col.label }}</span>
          <input
            type="checkbox"
            class="h-5 w-5 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-700"
            :data-testid="`usage-column-toggle-${col.key}`"
            :checked="isVisible(col.key)"
            @change="emit('toggle', col.key)"
          />
        </label>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" data-testid="usage-column-reset" :disabled="atDefault" @click="emit('reset')">
          {{ t('common.table.resetColumns') }}
        </button>
        <button type="button" class="btn btn-primary" @click="open = false">{{ t('common.table.done') }}</button>
      </template>
    </BottomSheet>
  </template>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { onClickOutside } from '@vueuse/core'
import BottomSheet from '@/components/common/BottomSheet.vue'
import Icon from '@/components/icons/Icon.vue'

/**
 * Which columns a usage list shows (admin and user pages, usage and errors tabs). The page keeps two memories of it
 * (useColumnVisibility: below 768px and from 768px, each with its defaults) and passes the one in use; this control
 * only lists the columns, toggles them and restores the defaults of the memory in use.
 */
withDefaults(defineProps<{
  columns: readonly { key: string; label: string }[]
  isVisible: (key: string) => boolean
  atDefault: boolean
  variant?: 'menu' | 'sheet'
}>(), {
  variant: 'menu',
})
const emit = defineEmits<{ toggle: [key: string]; reset: [] }>()

const { t } = useI18n()
const open = ref(false)
const menuRef = ref<HTMLElement | null>(null)
onClickOutside(menuRef, () => { open.value = false })
</script>
