<template>
  <Transition name="pending-changes">
    <button
      v-if="count > 0"
      type="button"
      class="pending-changes"
      v-tip="tip"
      data-test="pending-changes"
      @click="emit('merge')"
    >
      <StatusDot tone="info" class="pending-changes__dot" />
      <span>{{ label(countText) }}</span>
      <span class="pending-changes__act">· {{ t('common.pendingChanges.merge') }}</span>
    </button>
  </Transition>
</template>

<script setup lang="ts">
/**
 * "N changes · click to merge" for a table on the live engine (spec 7a, 7b). It floats and never takes layout space,
 * so coming and going moves nothing:
 * - from 1024px up it sits inside the table header's band (it may cover a column name or two), centred on the
 *   nearest positioned ancestor, which must start at the table's top edge (a `relative` box around the table whose
 *   scroll container keeps the header in view): it never drops onto the rows;
 * - below 1024px it is fixed just under the top bar.
 *
 * The caller words it (the usage page will say "N new records"), and `more` shows the count as "N+".
 *
 *   <div class="relative">
 *     <PendingChangesPill :count="table.pending.value" :more="table.pendingMore.value"
 *       :label="changesText" @merge="table.applyPending()" />
 *     <DataTable … />
 *   </div>
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { vTip } from '@/utils/tip'
import StatusDot from './StatusDot.vue'

const props = defineProps<{
  count: number
  more?: boolean
  /** The pill text for a count already written out ("3", "50+"). */
  label: (count: string) => string
  tip?: string
}>()
const emit = defineEmits<{ merge: [] }>()

const { t } = useI18n()

const countText = computed(() => formatPendingCount(props.count, props.more))
</script>

<script lang="ts">
/** The count as shown: "N", or "N+" when more are waiting than were counted. */
export const formatPendingCount = (count: number, more = false) => `${count}${more ? '+' : ''}`
</script>

<style scoped>
.pending-changes {
  position: fixed;
  z-index: 30;
  top: calc(var(--table-sticky-top, 4rem) + 8px);
  left: 50%;
  transform: translateX(-50%);
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 24px;
  padding: 0 12px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-full);
  background: var(--color-bg-raised);
  box-shadow: var(--shadow-popover);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  white-space: nowrap;
  transition: opacity var(--motion-exit) var(--ease-in), background-color var(--motion-fast) var(--ease-out);
}
/* Wide screens: inside the header band (40px header, 24px pill), above the sticky header cells. */
@media (min-width: 1024px) {
  .pending-changes {
    position: absolute;
    z-index: 240;
    top: calc((var(--header-height) - 24px) / 2);
  }
}
.pending-changes__dot {
  --dot-size: 6px;
}
.pending-changes__act {
  color: var(--color-text-secondary);
}
@media (hover: hover) {
  .pending-changes:hover {
    background: var(--color-bg-control-hover);
  }
  .pending-changes:hover .pending-changes__act {
    color: var(--color-text-primary);
  }
}
.pending-changes:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.pending-changes-enter-active {
  transition: opacity var(--motion-base) var(--ease-out);
}
.pending-changes-enter-from,
.pending-changes-leave-to {
  opacity: 0;
}
</style>
