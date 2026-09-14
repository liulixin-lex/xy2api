<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <div class="min-w-0">
        <p class="break-words text-sm font-medium text-gray-900 dark:text-gray-100">{{ account?.name }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqHistoryRetention') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="downloading || !account || !records.length" @click="downloadDiagnostics">
          <Icon name="download" size="sm" />{{ t('admin.accounts.iqDownload') }}
        </button>
        <button type="button" class="btn btn-secondary h-10 w-10 p-0" :disabled="loading || !account" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="loadRecords">
          <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
        </button>
      </div>
    </div>
    <p v-if="downloadError" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ downloadError }}</p>
    <div v-if="loading" role="status" class="flex items-center justify-center gap-2 py-12 text-sm text-gray-500 dark:text-dark-400"><Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}</div>
    <div v-else-if="error" role="alert" class="py-8 text-center">
      <p class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <button type="button" class="btn btn-secondary mt-3" @click="loadRecords">{{ t('common.refresh') }}</button>
    </div>
    <div v-else-if="!records.length" class="py-12 text-center text-gray-500 dark:text-dark-400"><Icon name="clock" class="mx-auto mb-3" /><p class="text-sm">{{ t('admin.accounts.iqEmpty') }}</p></div>
    <ol v-else class="divide-y divide-gray-200 dark:divide-dark-600">
      <li v-for="record in records" :key="record.id" class="py-5 first:pt-0 last:pb-0">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="inline-flex items-center rounded-md px-2 py-1 text-xs font-medium" :class="statusClass(record)">{{ record.finished_at ? t('admin.accounts.iqCheckStatus.' + record.status) : t('admin.accounts.iqRunning') }}</span>
          <time :datetime="record.started_at" class="text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatDateTime(record.started_at) }}</time>
        </div>
        <p v-if="record.reason" class="mt-3 break-words text-sm text-gray-700 dark:text-gray-300">{{ reasonLabel(record.reason) }}</p>
        <dl class="mt-4 grid grid-cols-2 gap-x-5 gap-y-4 sm:grid-cols-4">
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNormalizedAnswer') }}</dt><dd class="mt-1 break-words text-base font-semibold tabular-nums" data-testid="iq-answer">{{ record.normalized_answer ?? '—' }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqModel') }}</dt><dd class="mt-1 break-all text-sm text-gray-900 dark:text-gray-100">{{ record.model || '—' }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqEffort') }}</dt><dd class="mt-1 break-words text-sm text-gray-900 dark:text-gray-100">{{ effortLabel(record.effort) }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDuration') }}</dt><dd class="mt-1 text-sm tabular-nums text-gray-900 dark:text-gray-100">{{ record.finished_at ? (record.latency_ms / 1000).toFixed(2) + ' s' : '—' }}</dd></div>
        </dl>
        <details class="mt-4">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqRawAnswer') }}</summary>
          <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 text-sm text-gray-800 dark:bg-dark-700 dark:text-gray-200">{{ record.answer || t('admin.accounts.iqNoAnswer') }}</pre>
        </details>
        <details class="mt-3">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqDiagnostic') }}</summary>
          <p v-if="!record.grader_version || record.grader_version === 'candy-grader-v1'" class="input-hint">{{ t('admin.accounts.iqLegacyResult') }}</p>
          <p v-if="record.finished_at && record.format_compliant === false && record.normalized_answer != null" class="input-hint">{{ t('admin.accounts.iqFormatMismatch') }}</p>
          <dl class="mt-3 grid grid-cols-1 gap-x-4 gap-y-2 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqProtocol') }}</dt><dd>{{ record.protocol || '—' }}</dd>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqOutputMode') }}</dt><dd>{{ record.output_mode ? t(record.output_mode === 'strict' ? 'admin.accounts.iqStrict' : 'admin.accounts.iqCompat') : '—' }}</dd>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqGrader') }}</dt><dd>{{ record.grader_version || 'candy-grader-v1' }}</dd>
            <template v-if="record.reported_model"><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqReportedModel') }}</dt><dd class="break-all">{{ record.reported_model }}</dd></template>
            <template v-for="[key, value] in diagnosticEntries(record)" :key="key">
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDiagnosticLabels.' + key) }}</dt>
              <dd class="min-w-0 whitespace-pre-wrap break-all font-mono">{{ value }}</dd>
            </template>
          </dl>
        </details>
      </li>
    </ol>
  </BaseDialog>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
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
const effortLabel = (value: string) => te('admin.accounts.iqEfforts.' + value) ? t('admin.accounts.iqEfforts.' + value) : value || '—'
const statusClass = (record: IQCheckRecord) => !record.finished_at ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300' : record.status === 'smart' ? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300' : record.status === 'degraded' ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
const downloading = ref(false)
const downloadError = ref('')
let downloadRequest = 0
async function downloadDiagnostics() {
 if (!props.account || downloading.value) return
 const version = ++downloadRequest
 downloading.value = true
 downloadError.value = ''
 try { const blob = await downloadIQCheckDiagnostics(props.account.id); if (version !== downloadRequest) return; const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = 'iq-check-diagnostics.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000) }
 catch { if (version === downloadRequest) downloadError.value = t('admin.accounts.iqDownloadFailed') }
 finally { if (version === downloadRequest) downloading.value = false }
}
const records = ref<IQCheckRecord[]>([])
const loading = ref(false)
const error = ref('')
let request = 0
async function loadRecords() {
  const version = ++request
  const id = props.account?.id
  if (!props.show || !id) return
  loading.value = true
  error.value = ''
  try { const result = await adminAPI.accounts.getIQCheckResults(id); if (version === request) records.value = result }
  catch { if (version === request) error.value = t('admin.accounts.iqHistoryFailed') }
  finally { if (version === request) loading.value = false }
}
watch([() => props.show, () => props.account?.id], () => {
  request++; downloadRequest++
  records.value = []; error.value = ''; downloadError.value = ''
  loading.value = false; downloading.value = false
  void loadRecords()
}, { immediate: true })
onBeforeUnmount(() => { request++; downloadRequest++ })
</script>
