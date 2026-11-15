<template>
  <SchedQualityPanel
    :on-iq-check="onIqCheck"
    :on-edit="onEdit"
    :on-recovered="onRecovered"
    :account="account"
    :group-info="groupInfo"
    :anchor="anchor"
  />
  <GroupSchedBoard />
</template>

<script setup lang="ts">
import { onUnmounted } from 'vue'
import SchedQualityPanel from './SchedQualityPanel.vue'
import GroupSchedBoard from '@/components/admin/group/GroupSchedBoard.vue'
import { closeSchedBoard, closeSchedDrawer } from '@/composables/useSchedLayers'
import type { Account } from '@/types'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'

// Hosts the quality panel and the group board on a page. Leaving the page closes them.
defineProps<{
  onIqCheck?: (accountId: number) => void
  onEdit?: (accountId: number) => void
  /** Reads the account back on the page after a recovery from the panel. */
  onRecovered?: (accountId: number) => Promise<void> | void
  /** The page's row of an account, for the panel's platform line, upstream ratio and quality summary. */
  account?: (id: number) => (Account & { quality?: BackendAccountSummary | null }) | undefined
  /** Name and alias of a group, for the panel's alias pills. */
  groupInfo?: (id: number) => { name: string; alias?: string | null } | undefined
  /** The element whose top edge the panel's top follows (the table). */
  anchor?: () => HTMLElement | null
}>()

onUnmounted(() => {
  closeSchedDrawer()
  closeSchedBoard()
})
</script>
