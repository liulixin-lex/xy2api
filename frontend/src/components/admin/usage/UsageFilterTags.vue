<template>
  <span v-for="tag in tags" :key="tag.key" class="usage-tag" :data-key="tag.key" data-testid="usage-filter-tag">
    <span class="usage-tag__k">{{ tag.label }}：</span>
    <span class="usage-tag__v">{{ tag.text }}</span>
    <button
      type="button"
      class="usage-tag__x"
      :aria-label="t('usage.removeFilter', { label: tag.label })"
      @click="emit('remove', tag.key)"
    >
      <Icon name="x" size="xs" />
    </button>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

/**
 * The chosen filters of a usage list on a phone, as removable tags under the toolbar's first row (mobile list plan,
 * item 15), in the accounts toolbar's look. The page owns the filters: a tag only names one and asks to remove it.
 */
export interface UsageFilterTag {
  key: string
  label: string
  text: string
}

defineProps<{ tags: readonly UsageFilterTag[] }>()
const emit = defineEmits<{ remove: [key: string] }>()
const { t } = useI18n()
</script>

<style scoped>
@reference "../../../style.css";

.usage-tag {
  display: inline-flex;
  max-width: 100%;
  height: 24px;
  align-items: center;
  gap: 2px;
  padding: 0 2px 0 8px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  font-size: var(--font-size-small);
  white-space: nowrap;
}
.usage-tag__k {
  flex: none;
  color: var(--color-text-secondary);
}
.usage-tag__v {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-primary);
  text-overflow: ellipsis;
}
.usage-tag__x {
  position: relative;
  display: inline-grid;
  width: 20px;
  height: 20px;
  flex: none;
  place-items: center;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
}
/* A 40px target on touch without a bigger mark (the tags sit 6px apart, so the targets meet but do not overlap the
   next tag's text). */
@media (pointer: coarse) {
  .usage-tag__x::before {
    content: '';
    position: absolute;
    inset: -10px;
  }
}
.usage-tag__x:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  .usage-tag__x:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
</style>
