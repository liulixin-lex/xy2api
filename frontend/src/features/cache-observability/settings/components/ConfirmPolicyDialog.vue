<template>
  <BaseDialog :show="!!preview" :title="title" width="wide" :close-on-escape="!preview?.submitting" @close="cancel">
    <div v-if="preview" class="space-y-4 text-sm" data-test="confirm-dialog">
      <p v-if="preview.validating" class="flex items-center gap-2 text-gray-600 dark:text-dark-300" role="status" data-test="validating">
        <Icon name="refresh" size="sm" class="animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {{ t('admin.cacheSettings.dialog.validating') }}
      </p>

      <div v-else-if="preview.validateFailure" role="alert" class="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200" data-test="validate-failure">
        <p>{{ failureText(preview.validateFailure) }}</p>
        <button v-if="preview.validateFailure.kind === 'conflict'" type="button" class="btn btn-secondary btn-sm mt-2" @click="state.refreshConflict()">
          {{ t('admin.cacheSettings.dialog.refreshConflict') }}
        </button>
      </div>

      <template v-else-if="result">
        <div class="flex flex-wrap items-center gap-2">
          <span class="badge" :class="result.risk === 'low' ? 'badge-gray' : 'badge-danger'">{{ codeLabel('risk', result.risk, t, te) }}</span>
          <span class="badge badge-gray" data-test="direction">{{ codeLabel('direction', result.direction, t, te) }}</span>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full text-left">
            <caption class="sr-only">{{ t('admin.cacheSettings.dialog.diffCaption') }}</caption>
            <thead>
              <tr class="text-xs text-gray-500 dark:text-dark-400">
                <th scope="col" class="py-1 pr-3 font-medium">{{ t('admin.cacheSettings.dialog.field') }}</th>
                <th scope="col" class="py-1 pr-3 font-medium">{{ t('admin.cacheSettings.dialog.from') }}</th>
                <th scope="col" class="py-1 font-medium">{{ t('admin.cacheSettings.dialog.to') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="row in diffRows" :key="row.field" :data-test="`diff-${row.field}`">
                <th scope="row" class="py-2 pr-3 font-medium text-gray-900 dark:text-white">
                  {{ row.label }}
                  <code class="ml-1 text-xs font-normal text-gray-500 dark:text-dark-400">{{ row.field }}</code>
                </th>
                <td class="break-words py-2 pr-3 text-gray-600 dark:text-dark-300">{{ row.before }}</td>
                <td class="break-words py-2 font-medium text-gray-900 dark:text-white">{{ row.after }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <ul v-if="result.errors.length" class="space-y-1" role="list" data-test="validate-errors">
          <li v-for="(error, index) in result.errors" :key="index" class="flex items-start gap-2 text-red-700 dark:text-red-300">
            <Icon name="xCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
            {{ fieldLabel(error.field) }}{{ t('admin.cacheSettings.labelSeparator') }}{{ codeLabel('errors', error.code, t, te, { limit: error.limit ?? '-', min: '-', max: '-' }) }}
          </li>
        </ul>

        <ul v-if="result.warnings.length" class="space-y-1" role="list" data-test="validate-warnings">
          <li v-for="w in result.warnings" :key="w" class="flex items-start gap-2 text-amber-800 dark:text-amber-300">
            <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
            {{ codeLabel('warnings', w, t, te) }}
          </li>
        </ul>

        <p v-if="result.effective_preview" class="text-gray-700 dark:text-dark-200" data-test="effective-preview">
          {{ t('admin.cacheSettings.dialog.after', { state: codeLabel('states', result.effective_preview.state, t, te) }) }}
          <template v-for="b in result.effective_preview.blockers" :key="b">{{ t('admin.cacheSettings.listSeparator') }}{{ codeLabel('blockers', b, t, te) }}</template>
        </p>

        <p class="rounded-xl bg-gray-50 px-4 py-3 text-gray-700 dark:bg-dark-900/60 dark:text-dark-200">
          {{ t('admin.cacheSettings.dialog.effect', { seconds: state.refreshSeconds.value }) }}
        </p>

        <div v-if="result.valid">
          <label for="cache-policy-reason" class="input-label">
            {{ t('admin.cacheSettings.dialog.reason') }}
            <span class="font-normal text-gray-500 dark:text-dark-400">{{ result.confirm.reason_required ? t('admin.cacheSettings.dialog.required') : t('admin.cacheSettings.dialog.optional') }}</span>
          </label>
          <textarea
            id="cache-policy-reason"
            v-model="reason"
            class="input min-h-[4.5rem]"
            maxlength="200"
            :required="result.confirm.reason_required"
            :aria-required="result.confirm.reason_required || undefined"
            aria-describedby="cache-policy-reason-hint"
            :disabled="preview.submitting"
            data-test="reason"
          />
          <p id="cache-policy-reason-hint" class="input-hint">{{ t('admin.cacheSettings.dialog.reasonHint', { n: reason.length }) }}</p>
        </div>

        <label v-if="result.valid && result.confirm.checkbox_required" class="flex items-start gap-2 rounded-xl bg-red-50 px-4 py-3 text-red-800 dark:bg-red-950/30 dark:text-red-200">
          <input v-model="checked" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-red-300 text-red-600 focus:ring-red-500" :disabled="preview.submitting" data-test="confirm-checkbox" />
          <span>{{ t('admin.cacheSettings.dialog.checkbox') }}</span>
        </label>

        <div v-if="result.valid && result.confirm.type_to_confirm">
          <label for="cache-policy-typed" class="input-label">{{ t('admin.cacheSettings.dialog.typeToConfirm', { key: result.confirm.type_to_confirm }) }}</label>
          <input id="cache-policy-typed" v-model="typed" type="text" class="input font-mono sm:w-96" autocomplete="off" spellcheck="false" :disabled="preview.submitting" data-test="confirm-typed" />
        </div>
      </template>

      <div v-if="preview.failure" role="alert" class="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200" data-test="submit-failure">
        <p>{{ failureText(preview.failure) }}</p>
        <ul v-if="preview.failure.kind === 'invalid' && preview.failure.errors.length" class="mt-1 list-inside list-disc">
          <li v-for="(error, index) in preview.failure.errors" :key="index">{{ fieldLabel(error.field) }}{{ t('admin.cacheSettings.labelSeparator') }}{{ codeLabel('errors', error.code, t, te, { limit: error.limit ?? '-', min: '-', max: '-' }) }}</li>
        </ul>
        <button v-if="preview.failure.kind === 'conflict'" type="button" class="btn btn-secondary btn-sm mt-2" data-test="refresh-conflict" @click="state.refreshConflict()">
          {{ t('admin.cacheSettings.dialog.refreshConflict') }}
        </button>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button ref="cancelButton" type="button" class="btn btn-secondary" :disabled="preview?.submitting" @click="cancel">
          {{ t('admin.cacheSettings.dialog.cancel') }}
        </button>
        <button
          type="button"
          class="btn"
          :class="dangerous ? 'btn-danger' : 'btn-primary'"
          :disabled="!canSubmit"
          data-test="confirm-submit"
          @click="submit"
        >
          {{ submitLabel }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { fieldCopyKey, MASTER_KEY } from '../catalog'
import { codeLabel, formatUtcClock, formatValue } from '../format'
import { changedFields } from '../schema'
import type { PoliciesState } from '../usePolicies'
import type { WriteFailure } from '../types'

const props = defineProps<{ state: PoliciesState }>()
const { t, te } = useI18n()
const appStore = useAppStore()

const preview = computed(() => props.state.preview.value)
const result = computed(() => preview.value?.result ?? null)
const reason = ref('')
const checked = ref(false)
const typed = ref('')
const cancelButton = ref<HTMLButtonElement | null>(null)

watch(
  () => preview.value?.key,
  async (key) => {
    if (!key) return
    reason.value = ''
    checked.value = false
    typed.value = ''
    await nextTick()
    cancelButton.value?.focus()
  },
)

const title = computed(() => {
  const key = preview.value?.key
  if (!key) return ''
  const name = key === MASTER_KEY ? t('admin.cacheSettings.master.title') : t(`admin.cacheSettings.policies.${key}.title`)
  return t('admin.cacheSettings.dialog.title', { name })
})

function fieldLabel(path: string): string {
  const key = preview.value?.key ?? ''
  const copyKey = fieldCopyKey(key, path, 'label')
  return te(copyKey) ? t(copyKey) : path
}

function groupName(id: number): string {
  const group = props.state.groups.value.find((g) => g.id === id)
  return group ? `${group.name} (#${id})` : t('admin.cacheSettings.groupPicker.groupID', { id })
}

const diffRows = computed(() => {
  const state = preview.value
  if (!state) return []
  const schema = state.entry.schema
  const fromServer = result.value?.diff ?? []
  const rows = fromServer.length
    ? fromServer
    : changedFields(state.entry, props.state.draftOf(state.key)).map((c) => ({ field: c.field.field, before: c.before, after: c.after }))
  return rows.map((row) => {
    const field = schema.find((f) => f.field === row.field)
    const names = { group: groupName, account: (id: number) => t('admin.cacheSettings.ids.item', { id }) }
    return {
      field: row.field,
      label: fieldLabel(row.field),
      before: formatValue(field, row.before, t, te, names),
      after: formatValue(field, row.after, t, te, names),
    }
  })
})

const dangerous = computed(() => !!result.value && result.value.risk !== 'low' && result.value.direction !== 'disable')
const submitLabel = computed(() => {
  if (preview.value?.submitting) return t('admin.cacheSettings.dialog.submitting')
  return result.value?.direction === 'disable' ? t('admin.cacheSettings.dialog.submitDisable') : t('admin.cacheSettings.dialog.submit')
})
const canSubmit = computed(() => {
  const state = preview.value
  const r = result.value
  if (!state || !r || !r.valid || state.validating || state.submitting) return false
  if (state.failure?.kind === 'conflict') return false
  if (r.confirm.reason_required && !reason.value.trim()) return false
  if (r.confirm.checkbox_required && !checked.value) return false
  if (r.confirm.type_to_confirm && typed.value.trim() !== r.confirm.type_to_confirm) return false
  return true
})

function failureText(failure: WriteFailure): string {
  switch (failure.kind) {
    case 'conflict':
      return failure.currentRev === null
        ? t('admin.cacheSettings.dialog.conflictNoRev')
        : t('admin.cacheSettings.dialog.conflict', { rev: failure.currentRev })
    case 'invalid':
      return t('admin.cacheSettings.dialog.invalid')
    case 'confirm':
      return t('admin.cacheSettings.dialog.confirmRequired')
    case 'not_in_build':
      return t('admin.cacheSettings.dialog.notInBuild')
    default:
      return failure.message ? t('admin.cacheSettings.dialog.failedWith', { message: failure.message }) : t('admin.cacheSettings.dialog.failed')
  }
}

function cancel() {
  props.state.closePreview()
}

async function submit() {
  if (!canSubmit.value) return
  const key = preview.value?.key
  const ok = await props.state.submit({ reason: reason.value, checkbox: checked.value, typed: typed.value })
  if (ok && key) {
    const receipt = props.state.receipts.value[key]
    if (receipt) appStore.showSuccess(t('admin.cacheSettings.written', { rev: receipt.rev, time: formatUtcClock(receipt.effectiveBy) }))
  }
}
</script>
