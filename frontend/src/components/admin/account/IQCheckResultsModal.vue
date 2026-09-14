<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <p class="mb-3 text-sm">{{ account?.name }}</p>
    <button class="btn btn-secondary mb-3" :disabled="loading || downloading || !account" @click="downloadDiagnostics"><Icon name="download" size="sm" />{{ t('admin.accounts.iqDownload') }}</button>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="error" role="alert" class="text-red-600">{{ error }}</p>
    <p v-else-if="!records.length" class="text-gray-500">{{ t('admin.accounts.iqEmpty') }}</p>
    <ol v-else class="space-y-4">
      <li v-for="record in records" :key="record.id" class="border-t border-gray-200 pt-3 dark:border-dark-600">
        <div class="flex flex-wrap justify-between gap-2 text-sm"><time>{{ formatDateTime(record.started_at) }}</time><span :class="record.status === 'smart' ? 'text-green-600' : record.status === 'degraded' ? 'text-yellow-600' : 'text-gray-500'">{{ record.finished_at ? t(`admin.accounts.iqCheckStatus.${record.status}`) : t('admin.accounts.iqRunning') }}</span></div>
        <p class="mt-2 break-words text-xs text-gray-500">{{ record.model }} · {{ record.effort }} · {{ record.protocol || '—' }} · {{ record.output_mode || '—' }} · {{ record.latency_ms }} ms</p>
        <p v-if="record.reported_model" class="mt-1 break-words text-xs text-gray-500">{{ t('admin.accounts.iqReportedModel') }}: {{ record.reported_model }}</p>
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.accounts.iqGrader') }}: {{ record.grader_version || 'candy-grader-v1' }}</p>
        <p class="mt-2 text-sm">{{ t('admin.accounts.iqNormalizedAnswer') }}</p>
        <p class="mt-1 break-words text-lg font-semibold tabular-nums">{{ record.normalized_answer ?? '—' }}</p>
        <p v-if="!record.grader_version || record.grader_version === 'candy-grader-v1'" class="input-hint">{{ t('admin.accounts.iqLegacyResult') }}</p>
        <p v-if="record.finished_at && record.format_compliant === false && record.normalized_answer != null" class="input-hint">{{ t('admin.accounts.iqFormatMismatch') }}</p>
        <p v-if="record.reason" class="mt-2 text-sm">{{ t('admin.accounts.iqReason') }}: {{ reasonLabel(record.reason) }}</p>
        <details class="mt-2"><summary class="cursor-pointer text-sm">{{ t('admin.accounts.iqRawAnswer') }}</summary><pre class="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-3 text-sm dark:bg-dark-700">{{ record.answer || '—' }}</pre></details>
        <p v-if="record.finished_at && record.status === 'unknown' && record.diagnostic" class="mt-2 text-sm text-gray-500">{{ t('admin.accounts.iqDiagnosticHint') }}</p>
        <details v-if="record.diagnostic" class="mt-2">
          <summary class="cursor-pointer text-sm">{{ t('admin.accounts.iqDiagnostic') }}</summary>
          <dl class="mt-2 grid grid-cols-1 gap-x-4 gap-y-2 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
            <template v-for="[key, value] in diagnosticEntries(record)" :key="key">
              <dt class="text-gray-500">{{ t(`admin.accounts.iqDiagnosticLabels.${key}`) }}</dt>
              <dd class="min-w-0 whitespace-pre-wrap break-all font-mono">{{ value }}</dd>
            </template>
          </dl>
        </details>
      </li>
    </ol>
  </BaseDialog>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
import { adminAPI } from '@/api/admin'
import type { Account, IQCheckRecord } from '@/types'
import { formatDateTime } from '@/utils/format'
const props = defineProps<{ show: boolean; account: Account | null }>()
defineEmits<{ close: [] }>()
const { t, te } = useI18n()
const reasonLabel = (reason: string) => te(`admin.accounts.iqReasons.${reason}`) ? t(`admin.accounts.iqReasons.${reason}`) : reason
const diagnosticKeys = ['parser_version', 'stage', 'code', 'http_status', 'media_type', 'content_encoding', 'protocol', 'transport', 'format_detected', 'event_type', 'event_index', 'field', 'offset', 'bytes_read', 'request_id', 'error_code', 'error_type', 'retry_after', 'retry_after_unbounded', 'input_tokens', 'output_tokens', 'reasoning_tokens', 'retry_visibility'] as const
const diagnosticEntries = (record: IQCheckRecord) => diagnosticKeys.flatMap(key => {
  const value = record.diagnostic?.[key]
  return value == null || value === '' || value === false ? [] : [[key, String(value)]]
})
const downloading = ref(false)
async function downloadDiagnostics() {
 if (!props.account || downloading.value) return
 downloading.value = true
 try { const blob = await downloadIQCheckDiagnostics(props.account.id); const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = 'iq-check-diagnostics.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000) }
 catch { error.value = t('admin.accounts.iqFailed') }
 finally { downloading.value = false }
}
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
