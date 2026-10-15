<template>
  <div v-if="state && state !== 'OK'" class="flex flex-wrap items-center gap-1.5" data-testid="account-balance-state">
    <span
      :class="['inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium', stateClass]"
      :title="title"
    >
      {{ t('admin.resilience.account.balance') }} · {{ stateLabel }}
    </span>
    <button
      type="button"
      class="rounded px-1.5 py-0.5 text-xs text-primary-600 hover:bg-primary-50 disabled:opacity-50 dark:text-primary-400 dark:hover:bg-primary-900/20"
      :disabled="rechecking"
      :title="t('admin.resilience.account.recheckHint')"
      data-testid="account-balance-recheck"
      @click="recheck"
    >
      {{ rechecking ? t('admin.resilience.account.rechecking') : t('admin.resilience.account.recheck') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { BalanceRecord } from '@/api/admin/resilience'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  accountId: number
  record?: BalanceRecord | null
}>()

const emit = defineEmits<{
  (e: 'updated', record: BalanceRecord): void
  (e: 'error', message: string): void
  (e: 'success', message: string): void
}>()

const { t, te } = useI18n()
const rechecking = ref(false)

const state = computed(() => props.record?.state ?? '')

const stateLabel = computed(() => {
  const key = `admin.resilience.panel.states.${state.value}`
  return te(key) ? t(key) : state.value
})

const stateClass = computed(() => {
  switch (state.value) {
    case 'EXHAUSTED': return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
    case 'SUSPECT': return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
    default: return 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-300'
  }
})

// Go encodes an unset time as year 1; treat it as absent.
function validTime(value?: string): boolean {
  return !!value && new Date(value).getUTCFullYear() > 2000
}

const title = computed(() => {
  const parts: string[] = []
  if (validTime(props.record?.next_check_at)) {
    parts.push(t('admin.resilience.account.nextCheck', { time: formatDateTime(props.record?.next_check_at) }))
  }
  if (props.record?.last_evidence) parts.push(props.record.last_evidence)
  return parts.join('\n')
})

async function recheck() {
  if (rechecking.value) return
  rechecking.value = true
  try {
    const record = await adminAPI.resilience.recheckBalance(props.accountId)
    emit('updated', record)
    const key = `admin.resilience.panel.states.${record.state}`
    emit('success', t('admin.resilience.account.recheckDone', { state: te(key) ? t(key) : record.state }))
  } catch (cause) {
    emit('error', extractApiErrorMessage(cause, t('admin.resilience.account.recheckFailed')))
  } finally {
    rechecking.value = false
  }
}
</script>
