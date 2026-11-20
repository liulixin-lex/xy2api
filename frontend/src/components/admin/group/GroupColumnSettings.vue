<template>
  <div role="group" :aria-label="t('common.table.columnsTitle')" class="flex flex-col [@media(pointer:coarse)]:gap-2" data-test="group-column-settings">
    <!-- A multi-select list: a shown column is told by its brand check alone (12.6-5). -->
    <button
      v-for="col in columns"
      :key="col.key"
      type="button"
      role="menuitemcheckbox"
      :aria-checked="isVisible(col.key) ? 'true' : 'false'"
      class="gcol-item flex w-full items-center justify-between px-4 py-2 text-left text-sm text-gray-700 hover:bg-surface-popover-hover dark:text-gray-300"
      :data-test="`group-column-${col.key}`"
      @click="emit('toggle', col.key)"
    >
      <span>{{ col.label }}</span>
      <Icon v-if="isVisible(col.key)" name="check" size="sm" class="text-primary-500" :stroke-width="2" />
    </button>
    <template v-if="!atDefault">
      <div class="my-1 border-t border-line-popover"></div>
      <button
        type="button"
        class="gcol-item flex w-full items-center px-4 py-2 text-left text-sm text-gray-700 hover:bg-surface-popover-hover dark:text-gray-300"
        data-test="group-columns-reset"
        @click="emit('reset')"
      >{{ t('common.table.resetColumns') }}</button>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The groups page's column settings, one list for the wide dropdown and the phone's bottom sheet: each width keeps
 * its own shown columns and its own "restore defaults" (useColumnVisibility), offered once something differs.
 */
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

defineProps<{
  columns: { key: string; label: string }[]
  isVisible: (key: string) => boolean
  atDefault: boolean
}>()
const emit = defineEmits<{ toggle: [key: string]; reset: [] }>()
const { t } = useI18n()
</script>

<style scoped>
@media (pointer: coarse) {
  .gcol-item {
    min-height: 40px;
  }
}
</style>
