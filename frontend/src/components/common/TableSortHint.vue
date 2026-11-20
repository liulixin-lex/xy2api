<template>
  <span class="table-sort-hint" data-test="table-sort-hint">
    <span>{{ t('common.table.sortedBy', { label }) }}</span>
    <Icon :name="order === 'asc' ? 'arrowUp' : 'arrowDown'" size="xs" aria-hidden="true" />
    <span class="sr-only">{{ order === 'asc' ? t('common.table.ascending') : t('common.table.descending') }}</span>
  </span>
</template>

<script setup lang="ts">
/**
 * The list's sort when its column is not shown, so no header can tell it (mobile list plan): "Sorted by Last used ↓"
 * in the toolbar. The page shows it only while the sort key's column is hidden.
 */
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

defineProps<{ label: string; order: 'asc' | 'desc' }>()
const { t } = useI18n()
</script>

<style scoped>
.table-sort-hint {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 2px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  white-space: nowrap;
}
</style>
