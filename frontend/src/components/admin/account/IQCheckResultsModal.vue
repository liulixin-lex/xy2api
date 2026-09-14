<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <p class="mb-3 text-sm">{{ account?.name }}</p>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="error" role="alert" class="text-red-600">{{ error }}</p>
    <p v-else-if="!records.length" class="text-gray-500">{{ t('admin.accounts.iqEmpty') }}</p>
    <ol v-else class="space-y-4">
      <li v-for="record in records" :key="record.id" class="border-t border-gray-200 pt-3 dark:border-dark-600">
        <div class="flex flex-wrap justify-between gap-2 text-sm"><time>{{ formatDateTime(record.started_at) }}</time><span :class="record.status === 'smart' ? 'text-green-600' : record.status === 'degraded' ? 'text-yellow-600' : 'text-gray-500'">{{ t(`admin.accounts.iqCheckStatus.${record.status}`) }}</span></div>
        <p class="mt-2 text-xs text-gray-500">{{ record.model }} · {{ record.effort }} · {{ record.latency_ms }} ms</p>
        <p class="mt-2 text-sm">{{ t('admin.accounts.iqAnswer') }}</p>
        <pre class="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-3 text-sm dark:bg-dark-700">{{ record.answer || '—' }}</pre>
        <p v-if="record.reason" class="mt-2 text-sm">{{ t('admin.accounts.iqReason') }}: {{ record.reason }}</p>
      </li>
    </ol>
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import type { Account, IQCheckRecord } from '@/types'
import { formatDateTime } from '@/utils/format'
const props = defineProps<{ show: boolean; account: Account | null }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const records = ref<IQCheckRecord[]>([])
const loading = ref(false)
const error = ref('')
let request = 0
watch([() => props.show, () => props.account?.id], async ([show, id]) => {
  const version = ++request
  records.value = []; error.value = ''
  if (!show || !id) return
  loading.value = true
  try { const result = await adminAPI.accounts.getIQCheckResults(Number(id)); if (version === request) records.value = result }
  catch { if (version === request) error.value = t('admin.accounts.iqFailed') }
  finally { if (version === request) loading.value = false }
}, { immediate: true })
</script>
