<template>
  <SchedAccountDrawer :on-iq-check="onIqCheck" :on-edit="onEdit" :on-recovered="onRecovered" />
  <GroupSchedBoard />
</template>

<script setup lang="ts">
import { onUnmounted } from 'vue'
import SchedAccountDrawer from './SchedAccountDrawer.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import { closeSchedBoard, closeSchedDrawer } from '@/composables/useSchedLayers'

// Hosts the quality drawer and the group board on a page. Leaving the page closes them.
defineProps<{
  onIqCheck?: (accountId: number) => void
  onEdit?: (accountId: number) => void
  /** Reads the account back on the page after a recovery from the drawer. */
  onRecovered?: (accountId: number) => Promise<void> | void
}>()

onUnmounted(() => {
  closeSchedDrawer()
  closeSchedBoard()
})
</script>
