<template>
  <BaseDialog :show="!!preview" :title="title" width="wide" :close-on-escape="!preview?.submitting" @close="cancel">
    <div v-if="preview" class="space-y-4 text-sm" data-test="confirm-dialog">
      <section
        v-if="conflict"
        class="space-y-3 rounded-xl bg-sky-50 px-4 py-3 text-sky-950 ring-1 ring-inset ring-sky-600/20 dark:bg-sky-900/20 dark:text-sky-100 dark:ring-sky-400/30"
        aria-labelledby="cache-conflict-title"
        data-test="conflict"
      >
        <h3 id="cache-conflict-title" class="font-semibold">
          {{ t('admin.cacheSettings.conflict.title', { base: conflict.baseRev, rev: conflict.latestRev }) }}
        </h3>
        <div v-if="conflict.writes && conflict.writes.length" data-test="conflict-writes">
          <p v-if="conflict.writes.length > 1" class="text-xs">{{ t('admin.cacheSettings.conflict.count', { n: conflict.writes.length }) }}</p>
          <ol class="space-y-1.5" role="list">
            <li v-for="write in conflict.writes" :key="`${write.at}-${write.rev_after}`">{{ writeText(write.actor?.email, write.at, write.rev_after, write.changed_fields, write.reason) }}</li>
          </ol>
        </div>
        <p v-else data-test="conflict-latest">
          {{ writeText(conflict.latest.updated_by?.email, conflict.latest.updated_at, conflict.latestRev, conflict.theirFields, conflict.latest.reason) }}
        </p>
        <p v-if="conflict.merged.length" data-test="conflict-merged">{{ t('admin.cacheSettings.conflict.merged', { fields: names(preview.key, conflict.merged) }) }}</p>

        <fieldset
          v-for="overlap in conflict.overlaps"
          :key="overlap.field"
          class="rounded-lg bg-white p-3 ring-2 ring-amber-400 dark:bg-dark-800 dark:ring-amber-500/70"
          :data-test="`overlap-${overlap.field}`"
        >
          <legend class="sr-only">{{ labelOf(preview.key, overlap.field) }}</legend>
          <p class="font-medium text-gray-900 dark:text-white" aria-hidden="true">{{ labelOf(preview.key, overlap.field) }}</p>
          <p class="mt-0.5 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.conflict.was', { value: show(preview.key, overlap.field, overlap.base) }) }}</p>
          <p v-if="overlap.reverting" class="mt-2 flex items-start gap-1.5 font-medium text-red-700 dark:text-red-300" data-test="overlap-reverting">
            <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
            {{ t('admin.cacheSettings.conflict.reverting') }}
          </p>
          <div class="mt-2 grid gap-2 sm:grid-cols-2">
            <label
              v-for="side in sides"
              :key="side"
              class="flex cursor-pointer items-start gap-2 rounded-lg px-3 py-2 ring-1 ring-inset"
              :class="overlap.choice === side ? 'bg-primary-50 ring-primary-500 dark:bg-primary-900/20' : 'ring-gray-200 dark:ring-dark-600'"
            >
              <input
                type="radio"
                class="mt-0.5 h-4 w-4 border-gray-300 text-primary-600 focus:ring-0 focus-visible:ring-2 focus-visible:ring-primary-500 dark:border-dark-500"
                :name="`cache-overlap-${overlap.field}`"
                :value="side"
                :checked="overlap.choice === side"
                :disabled="!preview.pendingDraft"
                :data-test="`choose-${side}-${overlap.field}`"
                @change="state.setChoice(overlap.field, side)"
              />
              <span class="min-w-0 text-gray-800 dark:text-gray-100">
                {{ t(`admin.cacheSettings.conflict.${side}Label`, { value: show(preview.key, overlap.field, overlap[side]) }) }}
              </span>
            </label>
          </div>
        </fieldset>

        <div v-if="preview.pendingDraft" class="flex flex-wrap items-center gap-3">
          <button type="button" class="btn btn-primary btn-sm" :disabled="unresolved > 0" data-test="resolve-conflict" @click="state.resolveConflict()">
            {{ t('admin.cacheSettings.conflict.apply') }}
          </button>
          <span v-if="unresolved" class="text-xs text-gray-700 dark:text-dark-200">{{ t('admin.cacheSettings.conflict.remaining', { n: unresolved }) }}</span>
        </div>
        <p v-if="preview.nothingLeft" class="font-medium" data-test="nothing-left">{{ t('admin.cacheSettings.conflict.nothingLeft') }}</p>
      </section>

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
          <span class="badge" :class="riskBadge.cls" data-test="dialog-risk">{{ riskBadge.text }}</span>
          <span class="badge badge-gray" data-test="direction">{{ codeLabel('direction', result.direction, t, te) }}</span>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full text-left">
            <caption class="sr-only">{{ t('admin.cacheSettings.dialog.diffCaption') }}</caption>
            <thead>
              <tr class="text-xs text-gray-600 dark:text-dark-300">
                <th scope="col" class="py-1 pr-3 font-medium">{{ t('admin.cacheSettings.dialog.field') }}</th>
                <th scope="col" class="py-1 pr-3 font-medium">{{ t('admin.cacheSettings.dialog.from') }}</th>
                <th scope="col" class="py-1 font-medium">{{ t('admin.cacheSettings.dialog.to') }}</th>
              </tr>
            </thead>
            <tbody v-for="row in diffRows" :key="row.field" class="border-t border-gray-100 dark:border-dark-700">
              <tr :data-test="`diff-${row.field}`">
                <th scope="row" class="pr-3 pt-2 font-medium text-gray-900 dark:text-white" :class="row.impact ? 'pb-0.5' : 'pb-2'">{{ row.label }}</th>
                <td class="break-words pr-3 pt-2 text-gray-600 dark:text-dark-300" :class="row.impact ? 'pb-0.5' : 'pb-2'">{{ row.before }}</td>
                <td class="break-words pt-2 font-medium text-gray-900 dark:text-white" :class="row.impact ? 'pb-0.5' : 'pb-2'">{{ row.after }}</td>
              </tr>
              <tr v-if="row.impact" :data-test="`impact-${row.field}`">
                <td colspan="3" class="pb-2 text-xs text-gray-700 dark:text-dark-200">
                  <span class="font-semibold">{{ t('admin.cacheSettings.impact') }}</span>{{ row.impact }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <ul v-if="result.errors.length" class="space-y-1" role="list" data-test="validate-errors">
          <li v-for="(error, index) in result.errors" :key="index" class="flex items-start gap-2 text-red-700 dark:text-red-300">
            <Icon name="xCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
            {{ errorText(error) }}
          </li>
        </ul>

        <section v-if="result.valid" class="space-y-2 rounded-xl bg-gray-50 px-4 py-3 text-gray-700 dark:bg-dark-900/60 dark:text-dark-200" aria-labelledby="cache-after-title" data-test="after-submit">
          <h3 id="cache-after-title" class="font-semibold text-gray-900 dark:text-white">{{ t('admin.cacheSettings.dialog.afterTitle') }}</h3>
          <p v-if="state.killSwitch.value" class="flex items-start gap-2 font-medium text-red-800 dark:text-red-200" data-test="kill-note">
            <Icon name="ban" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
            {{ t('admin.cacheSettings.dialog.killNote') }}
          </p>
          <template v-if="isMaster">
            <p class="font-medium text-gray-900 dark:text-white">
              {{ masterAfter ? t('admin.cacheSettings.dialog.masterOnLead', { seconds: state.refreshSeconds.value }) : t('admin.cacheSettings.dialog.masterOffLead', { seconds: state.refreshSeconds.value }) }}
            </p>
            <ul v-if="gatedRows.length" class="space-y-1.5" role="list" data-test="gated-impact">
              <li v-for="row in gatedRows" :key="row.key" class="flex flex-wrap items-baseline gap-x-2" :data-test="`affected-${row.key}`">
                <span class="font-medium text-gray-900 dark:text-white">{{ row.name }}</span>
                <span class="text-xs">{{ row.summary }}</span>
                <span v-if="row.change" class="tabular-nums">{{ row.change }}</span>
              </li>
            </ul>
            <p v-if="gatedMissing.length" class="text-xs">{{ t('admin.cacheSettings.notIncluded', { names: gatedMissing.join(t('admin.cacheSettings.nameSeparator')) }) }}</p>
          </template>
          <p v-else-if="selfAfter" data-test="effective-preview">
            {{ t('admin.cacheSettings.dialog.after', { state: codeLabel('states', selfAfter, t, te) }) }}
            <template v-for="b in result.effective_preview?.blockers ?? []" :key="b">{{ t('admin.cacheSettings.listSeparator') }}{{ codeLabel('blockers', b, t, te) }}</template>
          </p>
          <ul v-if="result.warnings.length" class="space-y-1" role="list" data-test="validate-warnings">
            <li v-for="w in result.warnings" :key="w" class="flex items-start gap-2 text-amber-800 dark:text-amber-300">
              <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
              {{ codeLabel('warnings', w, t, te) }}
            </li>
          </ul>
          <p>{{ t('admin.cacheSettings.dialog.effect', { seconds: state.refreshSeconds.value }) }}</p>
          <p data-test="undo">{{ undoText }}</p>
        </section>

        <div v-if="result.valid">
          <label for="cache-policy-reason" class="input-label">
            {{ t('admin.cacheSettings.dialog.reason') }}
            <span class="font-normal text-gray-600 dark:text-dark-300">{{ result.confirm.reason_required ? t('admin.cacheSettings.dialog.required') : t('admin.cacheSettings.dialog.optional') }}</span>
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

        <!-- Neutral until something is wrong: the box asks for a check, it is not an error (M-4). -->
        <div v-if="result.valid && result.confirm.checkbox_required" class="rounded-xl px-4 py-3 ring-1 ring-inset ring-gray-300 dark:ring-dark-600" data-test="confirm-check">
          <label class="flex items-start gap-2 text-gray-900 dark:text-gray-100">
            <input
              v-model="checked"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-400 text-primary-600 focus:ring-0 focus-visible:ring-2 focus-visible:ring-primary-500 dark:border-dark-500"
              :disabled="preview.submitting"
              :aria-describedby="checklist.length > 1 ? 'cache-policy-checklist' : undefined"
              data-test="confirm-checkbox"
            />
            <span class="font-medium">{{ checkboxLabel }}</span>
          </label>
          <ul v-if="checklist.length > 1" id="cache-policy-checklist" class="mt-2 list-disc space-y-0.5 pl-10 text-gray-700 dark:text-dark-200" role="list" data-test="checklist">
            <li v-for="item in checklist" :key="item">{{ item }}</li>
          </ul>
        </div>

        <div v-if="result.valid && result.confirm.type_to_confirm">
          <label for="cache-policy-typed" class="input-label">{{ t('admin.cacheSettings.dialog.typeToConfirm', { key: result.confirm.type_to_confirm }) }}</label>
          <input id="cache-policy-typed" v-model="typed" type="text" class="input font-mono sm:w-96" autocomplete="off" spellcheck="false" :disabled="preview.submitting" data-test="confirm-typed" />
        </div>
      </template>

      <div v-if="preview.failure" role="alert" class="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200" data-test="submit-failure">
        <p>{{ failureText(preview.failure) }}</p>
        <ul v-if="preview.failure.kind === 'invalid' && preview.failure.errors.length" class="mt-1 list-inside list-disc">
          <li v-for="(error, index) in preview.failure.errors" :key="index">{{ errorText(error) }}</li>
        </ul>
        <button v-if="preview.failure.kind === 'conflict' && !preview.validating" type="button" class="btn btn-secondary btn-sm mt-2" data-test="refresh-conflict" @click="state.refreshConflict()">
          {{ t('admin.cacheSettings.dialog.refreshConflict') }}
        </button>
      </div>
    </div>

    <template #footer>
      <div class="flex flex-wrap items-center justify-end gap-3">
        <p v-if="stillNeeded.length" id="cache-policy-submit-hint" class="mr-auto text-xs text-gray-700 dark:text-dark-200" data-test="submit-hint">
          {{ t('admin.cacheSettings.dialog.stillNeeded', { items: stillNeeded.join(t('admin.cacheSettings.nameSeparator')) }) }}
        </p>
        <button ref="cancelButton" type="button" class="btn btn-secondary" :disabled="preview?.submitting" data-test="confirm-cancel" @click="cancel">
          {{ preview?.nothingLeft ? t('admin.cacheSettings.dialog.close') : t('admin.cacheSettings.dialog.cancel') }}
        </button>
        <button
          v-if="!preview?.nothingLeft"
          type="button"
          class="btn"
          :class="dangerous ? 'btn-danger' : 'btn-primary'"
          :disabled="!canSubmit"
          :aria-describedby="stillNeeded.length ? 'cache-policy-submit-hint' : undefined"
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
import { configuredFields } from '../effects'
import { codeLabel, errorParams, formatBoth, formatLocalClock, formatUtcClock, formatValue, idNames } from '../format'
import { changedFields, fieldKind, getPath } from '../schema'
import type { PoliciesState } from '../usePolicies'
import type { FieldError, WriteFailure } from '../types'

const props = defineProps<{ state: PoliciesState }>()
const { t, te } = useI18n()
const appStore = useAppStore()

const preview = computed(() => props.state.preview.value)
const result = computed(() => preview.value?.result ?? null)
const conflict = computed(() => preview.value?.conflict ?? null)
const isMaster = computed(() => preview.value?.key === MASTER_KEY)
const reason = ref('')
const checked = ref(false)
const typed = ref('')
const cancelButton = ref<HTMLButtonElement | null>(null)
const sides = ['theirs', 'mine'] as const

// A new key resets the inputs and focuses Cancel.
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
// After a merge the content changed: confirm again, but keep the typed reason (§11.7 merge step 4).
watch(
  () => conflict.value?.latestRev,
  (rev) => {
    if (rev === undefined) return
    checked.value = false
    typed.value = ''
  },
)

const title = computed(() => {
  const key = preview.value?.key
  if (!key) return ''
  const name = key === MASTER_KEY ? t('admin.cacheSettings.master.title') : t(`admin.cacheSettings.policies.${key}.title`)
  return t('admin.cacheSettings.dialog.title', { name })
})

function schemaField(key: string, path: string) {
  return props.state.policy(key)?.schema.find((f) => f.field === path)
}

function labelOf(key: string, path: string): string {
  if (path === 'reason') return t('admin.cacheSettings.dialog.reason')
  if (!path) return t('admin.cacheSettings.dialog.request')
  const copyKey = fieldCopyKey(key, path, 'label')
  return te(copyKey) ? t(copyKey) : path
}

function fieldImpact(key: string, path: string): string {
  const copyKey = fieldCopyKey(key, path, 'risk')
  return te(copyKey) ? t(copyKey) : ''
}

function names(key: string, paths: string[]): string {
  return paths.map((p) => labelOf(key, p)).join(t('admin.cacheSettings.nameSeparator'))
}

function show(key: string, path: string, value: unknown): string {
  const field = schemaField(key, path)
  return formatValue(field, value, t, te, idNames(field, props.state.groups.value, t))
}

function errorText(error: FieldError): string {
  const key = preview.value?.key ?? ''
  const params = errorParams(schemaField(key, error.field), error)
  return `${labelOf(key, error.field)}${t('admin.cacheSettings.labelSeparator')}${codeLabel('errors', error.code, t, te, params)}`
}

/** One line per intervening write: who, when (local and UTC), which fields, why. Built as one string so no stray spaces appear. */
function writeText(actor: string | null | undefined, at: string | null, rev: number | null, fields: string[], reason: string | null): string {
  const key = preview.value?.key ?? ''
  const sep = t('admin.cacheSettings.listSeparator')
  let text = t('admin.cacheSettings.conflict.write', { actor: actor || t('admin.cacheSettings.footer.unknownActor'), time: formatBoth(at, t), rev: rev ?? '-' })
  if (fields.length) text += sep + t('admin.cacheSettings.conflict.changed', { fields: names(key, fields) })
  if (reason) text += sep + t('admin.cacheSettings.footer.reason') + reason
  return text
}

const unresolved = computed(() => conflict.value?.overlaps.filter((o) => !o.choice).length ?? 0)

const diffRows = computed(() => {
  const state = preview.value
  if (!state) return []
  const fromServer = result.value?.diff ?? []
  const rows = fromServer.length
    ? fromServer
    : changedFields(state.entry, props.state.draftOf(state.key)).map((c) => ({ field: c.field.field, before: c.before, after: c.after }))
  return rows.map((row) => ({
    field: row.field,
    label: labelOf(state.key, row.field),
    before: show(state.key, row.field, row.before),
    after: show(state.key, row.field, row.after),
    // The master's consequences are listed per gated key; a field's impact describes turning it up, not off.
    impact: isMaster.value || result.value?.direction === 'disable' ? '' : fieldImpact(state.key, row.field),
  }))
})

const masterAfter = computed(() => {
  const enabled = preview.value?.spec.enabled
  return typeof enabled === 'boolean' ? enabled : props.state.masterOn.value
})

/** "Rollout 50%, 3 accounts" from the stored value, or "not configured, off by default". */
function configSummary(key: string): string {
  const e = props.state.policy(key)
  if (!e) return ''
  const fields = configuredFields(e)
  if (!fields.length) return t('admin.cacheSettings.dialog.notConfigured')
  const parts = fields.slice(0, 3).map((f) => {
    const value = getPath(e.stored.spec, f.field)
    const shown = fieldKind(f) === 'id_list' && Array.isArray(value) ? t('admin.cacheSettings.dialog.idCount', { n: value.length }) : show(key, f.field, value)
    return `${labelOf(key, f.field)} ${shown}`
  })
  if (fields.length > 3) parts.push(t('admin.cacheSettings.dialog.moreFields', { n: fields.length - 3 }))
  return parts.join(t('admin.cacheSettings.nameSeparator'))
}

// States after submit come from the server's `affected[]` (§7.4); the panel never predicts them.
const gatedRows = computed(() => {
  if (!isMaster.value) return []
  const affected = new Map((result.value?.affected ?? []).map((a) => [a.key, a]))
  return (props.state.data.value?.policies ?? [])
    .filter((p) => p.gated_by_master && props.state.policy(p.key))
    .map((p) => {
      const a = affected.get(p.key)
      const change = a
        ? t('admin.cacheSettings.dialog.stateChange', {
            from: codeLabel('states', a.state_before, t, te),
            to: codeLabel('states', props.state.killSwitch.value ? 'killed' : a.state_after, t, te),
          })
        : ''
      return { key: p.key, name: t(`admin.cacheSettings.policies.${p.key}.title`), summary: configSummary(p.key), change }
    })
})
const gatedMissing = computed(() =>
  (props.state.data.value?.policies ?? []).filter((p) => p.gated_by_master && !props.state.policy(p.key)).map((p) => t(`admin.cacheSettings.policies.${p.key}.title`)),
)
const selfAfter = computed(() => {
  const r = result.value
  if (!r) return null
  if (props.state.killSwitch.value) return 'killed'
  return (r.affected ?? []).find((a) => a.key === preview.value?.key)?.state_after ?? r.effective_preview?.state ?? null
})

const undoText = computed(() => {
  const state = preview.value
  if (!state || !result.value) return ''
  const seconds = props.state.refreshSeconds.value
  if (state.key === MASTER_KEY) return masterAfter.value ? t('admin.cacheSettings.undo.masterOff', { seconds }) : t('admin.cacheSettings.undo.masterOn')
  if (result.value.direction === 'disable') return t('admin.cacheSettings.undo.reenable')
  return state.entry.gated_by_master ? t('admin.cacheSettings.undo.gated', { seconds }) : t('admin.cacheSettings.undo.plain', { seconds })
})

/** Short "I understand that …" wording of a warning or blocker; the full text is listed above. */
function ack(code: string, group: 'warnings' | 'blockers'): string {
  const key = `admin.cacheSettings.acks.${code}`
  return te(key) ? t(key) : codeLabel(group, code, t, te)
}

// The checkbox names what it confirms: the server's warnings and the blockers of the preview (§11.7 step 3).
const checklist = computed(() => {
  const r = result.value
  if (!r) return []
  const items = r.warnings.map((w) => ack(w, 'warnings'))
  for (const b of r.effective_preview?.blockers ?? []) items.push(ack(b, 'blockers'))
  return [...new Set(items)]
})
const checkboxLabel = computed(() => {
  const items = checklist.value
  if (!items.length) return t('admin.cacheSettings.dialog.checkboxScope')
  if (items.length === 1) return t('admin.cacheSettings.dialog.checkboxOne', { item: items[0] })
  return t('admin.cacheSettings.dialog.checkboxItems', { n: items.length })
})

// Risk follows the direction: turning something off lowers risk and must not look alarming (M-4).
const riskBadge = computed(() => {
  const r = result.value
  if (!r || r.direction === 'disable') return { cls: 'badge-gray', text: t('admin.cacheSettings.dialog.riskDisable') }
  return { cls: r.risk === 'low' ? 'badge-gray' : 'badge-danger', text: codeLabel('risk', r.risk, t, te) }
})

const dangerous = computed(() => !!result.value && result.value.risk !== 'low' && result.value.direction !== 'disable')
const submitLabel = computed(() => {
  if (preview.value?.submitting) return t('admin.cacheSettings.dialog.submitting')
  if (props.state.killSwitch.value) return t('admin.cacheSettings.dialog.submitSaveOnly')
  return result.value?.direction === 'disable' ? t('admin.cacheSettings.dialog.submitDisable') : t('admin.cacheSettings.dialog.submit')
})
const canSubmit = computed(() => {
  const state = preview.value
  const r = result.value
  if (!state || !r || !r.valid || r.rev_stale || state.validating || state.submitting || state.pendingDraft || state.nothingLeft) return false
  if (state.failure?.kind === 'conflict') return false
  if (r.confirm.reason_required && !reason.value.trim()) return false
  if (r.confirm.checkbox_required && !checked.value) return false
  if (r.confirm.type_to_confirm && typed.value.trim() !== r.confirm.type_to_confirm) return false
  return true
})

// Says why the confirm button is still disabled when only the admin's own confirmations are missing.
const stillNeeded = computed(() => {
  const state = preview.value
  const r = result.value
  if (!state || !r || !r.valid || state.validating || state.submitting || state.pendingDraft || state.nothingLeft) return []
  const missing: string[] = []
  if (r.confirm.reason_required && !reason.value.trim()) missing.push('reason')
  if (r.confirm.checkbox_required && !checked.value) missing.push('checkbox')
  if (r.confirm.type_to_confirm && typed.value.trim() !== r.confirm.type_to_confirm) missing.push('typed')
  return missing.map((m) => codeLabel('missing', m, t, te))
})

function failureText(failure: WriteFailure): string {
  switch (failure.kind) {
    case 'conflict':
      return failure.currentRev === null
        ? t('admin.cacheSettings.dialog.conflictNoRev')
        : t('admin.cacheSettings.dialog.conflict', { rev: failure.currentRev })
    case 'invalid':
      return failure.errors.length ? t('admin.cacheSettings.dialog.invalid') : t('admin.cacheSettings.dialog.invalidNoDetail')
    case 'confirm': {
      const missing = failure.missing.map((m) => codeLabel('missing', m, t, te))
      return missing.length
        ? t('admin.cacheSettings.dialog.confirmMissing', { items: missing.join(t('admin.cacheSettings.nameSeparator')) })
        : t('admin.cacheSettings.dialog.confirmRequired')
    }
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
    if (receipt?.savedOnly) appStore.showSuccess(t('admin.cacheSettings.savedOnly', { rev: receipt.rev }))
    else if (receipt) {
      appStore.showSuccess(t('admin.cacheSettings.written', { rev: receipt.rev, time: formatUtcClock(receipt.effectiveBy), local: formatLocalClock(receipt.effectiveBy) }))
    }
  }
}
</script>
