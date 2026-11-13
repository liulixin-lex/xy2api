<template>
  <BaseDialog :show="phase === 'confirm'" :title="actionName" width="narrow" @close="close">
    <p class="text-sm text-gray-900 dark:text-gray-100">
      <span class="sq-text-3">{{ t('admin.schedQuality.col.account') }}</span>
      <span class="sq-num ml-2 font-medium">{{ accounts.length }}</span>
    </p>
    <template #footer>
      <button type="button" class="btn btn-secondary btn-sm" @click="close">{{ t('admin.schedQuality.action.cancel') }}</button>
      <button type="button" class="btn btn-primary btn-sm" data-sq-batch-confirm @click="confirm">{{ action === 'reset' ? t('admin.schedQuality.action.confirm') : actionName }}</button>
    </template>
  </BaseDialog>

  <SchedOverridePopover
    :show="phase === 'popover'"
    :mode="action === 'exempt' ? 'exempt' : 'demote'"
    :anchor="anchor"
    :groups="groups"
    :default-group-id="0"
    batch
    @close="close"
    @confirm="(request) => run(request)"
  />

  <BaseDialog :show="phase === 'results'" :title="actionName" width="narrow" @close="close">
    <table class="sq-table" data-sq-batch-results>
      <thead>
        <tr>
          <th scope="col">{{ t('admin.schedQuality.col.account') }}</th>
          <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.result') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.id" :data-sq-batch-row="row.id" :data-sq-batch-result="row.result ?? 'running'">
          <td class="max-w-[16rem] truncate">{{ row.name }}</td>
          <td class="sq-r">
            <span v-if="row.result === null" class="spinner ml-auto inline-block h-3.5 w-3.5 align-middle text-gray-400" aria-hidden="true" />
            <span v-else :class="RESULT_CLASS[row.result]">{{ t(RESULT_KEY[row.result]) }}</span>
          </td>
        </tr>
      </tbody>
    </table>
    <template #footer>
      <p class="mr-auto text-sm sq-num">
        <span class="sq-text-3">{{ t('admin.schedQuality.result.ok') }}</span><span class="ml-1.5 font-medium">{{ count('ok') }}</span>
        <span class="ml-4 sq-text-3">{{ t('admin.schedQuality.result.failed') }}</span><span class="ml-1.5 font-medium">{{ count('failed') }}</span>
      </p>
      <button type="button" class="btn btn-secondary btn-sm" @click="close">{{ t('admin.schedQuality.action.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import SchedOverridePopover from './SchedOverridePopover.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedOverrideRequest } from '@/types/schedQuality'
import { markSchedChanged } from '@/composables/useSchedLayers'
import { useAdminPermission } from '@/composables/useAdminPermission'

// Batch quality actions of the account list (design spec A.5, B.11). Reset confirms first, demote and exempt use the
// override popover. A confirmed reset (恢复) hands the accounts back (`recover`) and runs in the background
// with one toast (contract delta F3). The others call each account one by one, four at a time, and list it with its
// result. A run works on what it was started with (action, request, accounts), never on the props of a later opening.
// Closing the results stops it: accounts not yet called are "not run" and answers still on their way are dropped.
// Until those answers are in, the run is busy and no other batch can start.
export type SchedBatchAction = 'reset' | 'demote' | 'exempt'
type Result = 'ok' | 'failed' | 'skipped'
interface Row { id: number; name: string; result: Result | null }
interface Batch { action: SchedBatchAction; request: SchedOverrideRequest; rows: Row[]; next: number; stopped: boolean }

const CONCURRENCY = 4
const RESULT_KEY: Record<Result, string> = {
  ok: 'admin.schedQuality.result.ok',
  failed: 'admin.schedQuality.result.failed',
  skipped: 'admin.schedQuality.result.skipped'
}
const RESULT_CLASS: Record<Result, string> = { ok: 'sq-pass-word', failed: 'sq-fail-word text-sm font-semibold', skipped: 'sq-text-3' }

const props = defineProps<{
  action: SchedBatchAction | null
  accounts: Array<{ id: number; name: string }>
  anchor: HTMLElement | null
  /** Scope choices for demote / exempt: every OpenAI group. */
  groups: Array<{ id: number; name: string }>
}>()
const emit = defineEmits<{ close: []; done: []; busy: [running: boolean]; recover: [accountIds: number[]] }>()
const { t } = useI18n()
const { canManage } = useAdminPermission('accounts')

const phase = ref<'confirm' | 'popover' | 'results' | null>(null)
/** The run on screen; null before the first run of an opening. */
const batch = shallowRef<Batch | null>(null)
const rows = ref<Row[]>([])
const actionName = computed(() => {
  const action = batch.value?.action ?? props.action
  return action ? t(`admin.schedQuality.action.${action}`) : ''
})
const count = (result: Result) => rows.value.filter((row) => row.result === result).length
let running = 0

watch(() => props.action, (action) => {
  if (!action || running) return
  batch.value = null
  rows.value = []
  phase.value = action === 'reset' ? 'confirm' : 'popover'
}, { immediate: true })

// A confirmation not yet given closes when accounts:manage is withdrawn.
watch(canManage, (allowed) => {
  if (!allowed && (phase.value === 'confirm' || phase.value === 'popover')) close()
})

function confirm() {
  const ids = props.accounts.map((account) => account.id)
  close()
  if (canManage.value) emit('recover', ids)
}

async function callOne(id: number, request: SchedOverrideRequest): Promise<Result> {
  try {
    await schedQualityAPI.setOverride(id, request)
    return 'ok'
  } catch {
    return 'failed'
  }
}

async function run(request: SchedOverrideRequest) {
  const action = props.action
  if (!action || running || phase.value === 'results') return
  const current: Batch = { action, request, rows: props.accounts.map((account) => ({ ...account, result: null })), next: 0, stopped: false }
  batch.value = current
  rows.value = current.rows
  const live = rows.value
  phase.value = 'results'
  running++
  emit('busy', true)
  const worker = async () => {
    while (!current.stopped && current.next < live.length) {
      const index = current.next++
      const result = await callOne(live[index].id, current.request)
      if (!current.stopped) live[index].result = result
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(CONCURRENCY, live.length) }, worker))
  } finally {
    running--
    void markSchedChanged()
    emit('busy', false)
    emit('done')
  }
}

function close() {
  const current = batch.value
  if (current && !current.stopped && phase.value === 'results') {
    current.stopped = true
    rows.value.slice(current.next).forEach((row) => { row.result = 'skipped' })
  }
  phase.value = null
  emit('close')
}
</script>
