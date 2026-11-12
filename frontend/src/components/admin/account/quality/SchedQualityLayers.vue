<template>
  <SchedAccountDrawer :on-iq-check="onIqCheck" :on-edit="onEdit" />
  <GroupSchedBoard />
  <SchedResetDialog @done="(id) => emit('reset-done', id)" />
</template>

<script setup lang="ts">
import { onUnmounted } from 'vue'
import SchedAccountDrawer from './SchedAccountDrawer.vue'
import SchedResetDialog from './SchedResetDialog.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import { closeSchedBoard, closeSchedDrawer, closeSchedReset } from '@/composables/useSchedLayers'

// Hosts the quality drawer, the group board and the reset flow on a page. Leaving the page closes them.
defineProps<{
  onIqCheck?: (accountId: number) => void
  onEdit?: (accountId: number) => void
}>()
const emit = defineEmits<{ 'reset-done': [accountId: number] }>()

onUnmounted(() => {
  closeSchedDrawer()
  closeSchedBoard()
  closeSchedReset()
})
</script>
