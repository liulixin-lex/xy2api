<template>
  <BaseDialog :show="show" :title="title" placement="sheet" :close-on-click-outside="true" @close="emit('close')">
    <div class="bottom-sheet" :data-test="testId">
      <slot />
    </div>
    <template v-if="$slots.footer" #footer>
      <slot name="footer" />
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
/**
 * A panel along the bottom of a phone screen for a list page's filters or column settings (mobile list plan, item 15):
 * BaseDialog in its sheet placement, so focus stays inside while it is open, Escape or a tap on the dimmed page closes
 * it, and focus goes back to the button that opened it. The page fills it; changes apply at once, the sheet has no
 * "apply" of its own (the footer is for "clear all" and "done").
 */
import BaseDialog from './BaseDialog.vue'

defineProps<{
  show: boolean
  title: string
  testId?: string
}>()
const emit = defineEmits<{ close: [] }>()
</script>

<style scoped>
.bottom-sheet {
  display: grid;
  gap: 16px;
}
</style>
