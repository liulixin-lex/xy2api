<template>
  <span class="alias-pill" :class="{ 'alias-pill--latin': latin, 'alias-pill--dot': tone }" aria-hidden="true">
    <span v-if="tone" class="alias-pill__dot"><StatusDot :tone="tone" /></span>
    <span class="alias-pill__text">{{ text }}</span>
  </span>
</template>

<script setup lang="ts">
/**
 * A group's alias as a static pill (the quality panel's groups table, the group picker, the groups page): the same
 * width, background and text rules as the pills of the accounts page's groups column, without hit zones. With `tone`
 * it carries a status dot the way those pills do (the alias preview of the groups page's form). Text that does not
 * fit is cut inside the pill.
 */
import { computed } from 'vue'
import { groupPillText, isLatinPillText } from '@/utils/groupAlias'
import StatusDot, { type StatusDotTone } from './StatusDot.vue'

const props = defineProps<{ group: { name: string; alias?: string | null }; tone?: StatusDotTone }>()
const text = computed(() => groupPillText(props.group))
const latin = computed(() => isLatinPillText(text.value))
</script>

<style scoped>
.alias-pill {
  display: inline-flex;
  flex: none;
  width: var(--pill-width);
  height: var(--pill-height);
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  line-height: var(--pill-height);
  white-space: nowrap;
}
.alias-pill__text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-align: center;
}
.alias-pill__dot {
  display: grid;
  flex: none;
  width: var(--pill-dot-zone);
  place-items: center;
}
.alias-pill--dot .alias-pill__text {
  padding-right: 4px;
  text-align: left;
}
/* Latin text at 12px too (mobile list plan, item 19: no text under 12px); what does not fit is cut inside the pill. */
.alias-pill--latin {
  font-size: var(--font-size-small);
}
</style>
