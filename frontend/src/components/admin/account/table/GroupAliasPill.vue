<template>
  <span class="alias-pill" :class="{ 'alias-pill--latin': latin }" aria-hidden="true">{{ text }}</span>
</template>

<script setup lang="ts">
/**
 * A group's alias as a static pill (the quality panel's groups table, the group picker): the same width, background
 * and text rules as the pills of the groups column, without hit zones. Text that does not fit is cut inside the pill.
 */
import { computed } from 'vue'
import { groupPillText, isLatinPillText } from './accountGroups'

const props = defineProps<{ group: { name: string; alias?: string | null } }>()
const text = computed(() => groupPillText(props.group))
const latin = computed(() => isLatinPillText(text.value))
</script>

<style scoped>
.alias-pill {
  display: inline-block;
  flex: none;
  width: var(--pill-width);
  height: var(--pill-height);
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  line-height: var(--pill-height);
  text-align: center;
  white-space: nowrap;
}
.alias-pill--latin {
  font-size: var(--font-size-micro);
}
</style>
