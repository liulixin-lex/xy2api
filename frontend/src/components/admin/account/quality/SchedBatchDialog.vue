<template>
  <BaseDialog :show="phase === 'confirm'" :title="actionName" width="narrow" @close="close">
    <p class="text-sm text-gray-900 dark:text-gray-100">
      <span class="sq-text-3">{{ t('admin.schedQuality.col.account') }}</span>
      <span class="sq-num ml-2 font-medium">{{ accounts.length }}</span>
    </p>
    <template #footer>
      <button type="button" class="btn btn-secondary btn-sm" @click="close">{{ t('admin.schedQuality.action.cancel') }}</button>
      <button type="button" class="btn btn-primary btn-sm" data-sq-batch-confirm @click="run()">{{ actionName }}</button>
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
        <tr v-for="row in rows" :key="row.id" :data-sq-batch-row="row.id">
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
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import SchedOverridePopover from './SchedOverridePopover.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedOverrideRequest } from '@/types/schedQuality'
import { markSchedChanged } from '@/composables/useSchedLayers'

// Batch quality actions of the account list (design spec A.5, B.11). Reset and test confirm first, demote and exempt
// use the override popover; then each account is called one by one, four at a time, and listed with its result.
export type SchedBatchAction = 'reset' | 'test' | 'demote' | 'exempt'
type Result = 'ok' | 'failed' | 'testing'

const CONCURRENCY = 4
const RESULT_KEY: Record<Result, string> = {
  ok: 'admin.schedQuality.result.ok',
  failed: 'admin.schedQuality.result.failed',
  testing: 'admin.schedQuality.state.testing'
}
const RESULT_CLASS: Record<Result, string> = { ok: 'sq-pass-word', failed: 'sq-fail-word text-sm font-semibold', testing: 'sq-test' }

const props = defineProps<{
  action: SchedBatchAction | null
  accounts: Array<{ id: number; name: string }>
  anchor: HTMLElement | null
  /** Scope choices for demote / exempt: every OpenAI group. */
  groups: Array<{ id: number; name: string }>
}>()
const emit = defineEmits<{ close: []; done: [] }>()
const { t } = useI18n()

const phase = ref<'confirm' | 'popover' | 'results' | null>(null)
const rows = ref<Array<{ id: number; name: string; result: Result | null }>>([])
const actionName = computed(() => (props.action ? t(`admin.schedQuality.action.${props.action}`) : ''))
const count = (result: Result) => rows.value.filter((row) => row.result === result).length

watch(() => props.action, (action) => {
  if (!action) return
  rows.value = []
  phase.value = action === 'reset' || action === 'test' ? 'confirm' : 'popover'
}, { immediate: true })

async function callOne(id: number, request?: SchedOverrideRequest): Promise<Result> {
  try {
    if (props.action === 'reset') {
      await schedQualityAPI.reset(id)
      return 'ok'
    }
    if (props.action === 'test') {
      const { tests } = await schedQualityAPI.startTest(id)
      if (tests.some((ticket) => ticket.started)) return 'ok'
      return tests.length && tests.every((ticket) => ticket.why === 'already_running') ? 'testing' : 'failed'
    }
    await schedQualityAPI.setOverride(id, request!)
    return 'ok'
  } catch {
    return 'failed'
  }
}

async function run(request?: SchedOverrideRequest) {
  rows.value = props.accounts.map((account) => ({ ...account, result: null }))
  phase.value = 'results'
  let next = 0
  const worker = async () => {
    while (next < rows.value.length) {
      const row = rows.value[next++]
      row.result = await callOne(row.id, request)
    }
  }
  await Promise.all(Array.from({ length: Math.min(CONCURRENCY, rows.value.length) }, worker))
  markSchedChanged()
  emit('done')
}

function close() {
  phase.value = null
  emit('close')
}
</script>
