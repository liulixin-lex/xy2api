<template>
  <BaseDialog :show="show" :title="t('supportTickets.form.title')" width="normal" @close="emit('close')">
    <CreateTicketForm :summary="summary" :active="show" @cancel="emit('close')" @created="(detail) => emit('created', detail)" />
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { SupportTicketDetail, SupportTicketUserSummary } from '@/api/supportTickets'
import CreateTicketForm from './CreateTicketForm.vue'

defineProps<{ show: boolean; summary: SupportTicketUserSummary | null }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created', detail: SupportTicketDetail): void
}>()
const { t } = useI18n()
</script>
