<template>
  <span
    v-if="reason"
    v-tip="reason"
    class="cell-empty cell-empty--why"
    data-test="cell-empty"
    @click="showTipNow($event.currentTarget)"
  ><span aria-hidden="true">—</span><span class="sr-only">{{ reason }}</span></span>
  <span v-else class="cell-empty" data-test="cell-empty">—</span>
</template>

<script setup lang="ts">
/**
 * An empty table cell (mobile list plan, copy): one light "—". A cell the row has no such value for (`reason`, such as
 * "OpenAI accounts only") still shows a single "—" and tells why on hover or a tap; a screen reader reads the reason.
 * It is not a tab stop (a page of such rows would be twenty of them).
 */
import { showTipNow, vTip } from '@/utils/tip'

defineProps<{ reason?: string }>()
</script>

<style scoped>
.cell-empty {
  color: var(--color-text-muted);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
}
.cell-empty--why {
  cursor: help;
}
</style>
