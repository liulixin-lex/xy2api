<template>
  <section class="border-t border-gray-100 pt-4 dark:border-dark-700" data-sq-ttft-rule>
    <div class="mb-3">
      <div class="font-medium text-gray-900 dark:text-white">{{ t('admin.schedQuality.ttftRule.title') }}</div>
      <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.schedQuality.ttftRule.hint') }}</p>
    </div>
    <div v-if="conflict || saveFailed" class="mb-3 flex items-center gap-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 dark:border-red-500/30 dark:bg-red-500/10" role="status" data-sq-ttft-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <span v-if="saveMessage" class="text-sm text-gray-700 dark:text-gray-300">{{ saveMessage }}</span>
      <button v-if="conflict" type="button" class="btn btn-secondary btn-sm" data-sq-ttft-reload @click="reload">{{ t('admin.schedQuality.action.reload') }}</button>
    </div>
    <!-- A relaxation the confirm dialog declined: the values stay as typed and are marked until saved or put back. -->
    <div v-if="unsaved" class="mb-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300" role="status" data-sq-ttft-unsaved>
      {{ t('admin.schedQuality.ttftRule.unsaved') }}
    </div>
    <div v-if="loadFailed" class="flex items-center gap-3">
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="reload">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <div v-else-if="record" class="grid gap-3">
      <div v-for="field in FIELDS" :key="field" class="flex min-h-[40px] flex-wrap items-center justify-between gap-x-3 gap-y-1" :data-sq-ttft-field="field">
        <label :for="ids[field]" class="text-sm text-gray-700 dark:text-gray-300">
          {{ t(`admin.schedQuality.ttftRule.${field}`) }}
          <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t(`admin.schedQuality.ttftRule.${field}Hint`) }}</span>
        </label>
        <span class="flex items-center gap-2">
          <span v-if="!valid[field]" :id="`${ids[field]}-note`" class="sq-fail-word text-xs" data-sq-ttft-invalid>{{ t(`admin.schedQuality.ttftRule.legal.${field}`) }}</span>
          <span v-else-if="outside[field]" :id="`${ids[field]}-note`" class="text-xs text-amber-700 dark:text-amber-400" data-sq-ttft-outside>
            {{ t('admin.schedQuality.ttftRule.outside', { range: replayedRange(field) }) }}
          </span>
          <input
            :id="ids[field]"
            v-model="form[field]"
            type="number"
            step="1"
            :disabled="saving"
            :aria-invalid="!valid[field]"
            :aria-describedby="!valid[field] || outside[field] ? `${ids[field]}-note` : undefined"
            :placeholder="record.defaults ? display(record.defaults[field]) : undefined"
            :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid[field], 'border-amber-500 dark:border-amber-500': valid[field] && unsaved && changed(field) }]"
            :data-sq-ttft-input="field"
            :data-sq-ttft-unsaved-input="valid[field] && unsaved && changed(field) ? '' : undefined"
            @input="edited[field] = true"
          />
          <span class="w-16 text-sm sq-text-3">{{ t('admin.schedQuality.ttftRule.unit.percent') }}</span>
        </span>
      </div>
      <p v-if="record.defaults" class="text-xs text-gray-500 dark:text-gray-400" data-sq-ttft-defaults>
        {{ t('admin.schedQuality.ttftRule.defaults', {
          confidence: display(record.defaults.confidence),
          tolerance: display(record.defaults.tolerance)
        }) }}
      </p>
    </div>

    <ConfirmDialog
      :show="confirming"
      :title="t('admin.schedQuality.ttftRule.confirmTitle')"
      :message="t('admin.schedQuality.ttftRule.confirmMessage')"
      :confirm-text="t('admin.schedQuality.ttftRule.confirmSave')"
      danger
      @confirm="settleConfirm(true)"
      @cancel="settleConfirm(false)"
    />
  </section>
</template>

<script setup lang="ts">
/**
 * The first-output line's global settings in the scheduling card (GET | PUT /admin/sched-quality/ttft-rule),
 * built like the group dialog's quality gate block (GroupSchedPolicyField): loaded on its own, saved as a separate
 * request with the version when SettingsView has saved the page (`save()`); a 409 shows "失败 + 重新载入", any
 * other failure "失败" with the backend's message.
 *
 * Confidence and tolerance read as percentages; the margin over the peers is not shown (the rules in force do not
 * compare with peers) and goes back as loaded. Values outside the legal ranges (50 < confidence < 100,
 * 0 < tolerance < 100) frame the input red and are not saved; values outside the replayed range are allowed with a note that no data supports them. Raising any value
 * makes accounts less likely to go to the rear (relaxing): a pure or mixed relaxation asks first, since saving it
 * clears every first-output rear and trial and judges them again. Declining keeps the typed values, marked as not
 * saved, like a failed save keeps them.
 */
import { computed, reactive, ref, shallowRef, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendTTFTRule, SchedTTFTRuleValues } from '@/api/admin/schedQualityTypes'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  TTFT_RULE_FIELDS as FIELDS,
  outsideReplayed,
  ttftRuleChange,
  ttftRuleInputValid,
  ttftRulePercent as display,
  type TTFTRuleShownField
} from './schedTTFTRule'

const { t } = useI18n()
const appStore = useAppStore()
const ids = { confidence: `sq-ttft-c-${useId()}`, tolerance: `sq-ttft-p-${useId()}` }

const record = shallowRef<BackendTTFTRule | null>(null)
const loadFailed = ref(false)
// The last save stopped at the confirm dialog's cancel.
const declined = ref(false)
// A PUT is on its way: the inputs wait for it, so its answer (filled back in) cannot overwrite newer typing.
const saving = ref(false)
const conflict = ref(false)
const saveFailed = ref(false)
const saveMessage = ref('')
const form = reactive<Record<TTFTRuleShownField, string>>({ confidence: '', tolerance: '' })
// The inputs show rounded values; a field the user has not typed in keeps the API value as is.
const edited = reactive<Record<TTFTRuleShownField, boolean>>({ confidence: false, tolerance: false })

function fill(rule: BackendTTFTRule) {
  for (const field of FIELDS) {
    form[field] = display(rule[field])
    edited[field] = false
  }
}

async function reload() {
  loadFailed.value = false
  try {
    const value = await schedQualityAPI.getTTFTRule()
    record.value = value
    conflict.value = false
    saveFailed.value = false
    saveMessage.value = ''
    declined.value = false
    fill(value)
  } catch {
    loadFailed.value = true
  }
}
void reload()

const valid = computed(() => Object.fromEntries(FIELDS.map((field) => [field, ttftRuleInputValid(field, form[field])])) as Record<TTFTRuleShownField, boolean>)
// Nothing loaded (still loading, or the load failed and shows its retry): nothing to check or save, so the page saves.
const isValid = computed(() => !record.value || FIELDS.every((field) => valid.value[field]))

const current = computed<SchedTTFTRuleValues>(() => {
  const saved = record.value
  const value = (field: TTFTRuleShownField) => (saved && !edited[field] ? saved[field] : Number((Number(form[field]) / 100).toFixed(6)))
  // Not shown: the loaded value goes back as it is.
  return { confidence: value('confidence'), tolerance: value('tolerance'), peer_margin: saved?.peer_margin ?? 0 }
})

/** Declined, and the values still differ from the saved ones: the note and the marked inputs show. */
const unsaved = computed(() => declined.value && !!record.value && ttftRuleChange(record.value, current.value) !== 'none')
const changed = (field: TTFTRuleShownField) => !!record.value && current.value[field] !== record.value[field]

const outside = computed(() =>
  Object.fromEntries(FIELDS.map((field) => [field, outsideReplayed(record.value?.replayed?.[field], current.value[field])])) as Record<TTFTRuleShownField, boolean>
)

const replayedRange = (field: TTFTRuleShownField) => {
  const range = record.value?.replayed?.[field]
  return range ? t('admin.schedQuality.ttftRule.range.percent', { min: display(range[0]), max: display(range[1]) }) : ''
}

// ---- Relaxing asks first ----
const confirming = ref(false)
let settle: ((ok: boolean) => void) | null = null
const confirmRelax = () => new Promise<boolean>((resolve) => {
  settle = resolve
  confirming.value = true
})
function settleConfirm(ok: boolean) {
  confirming.value = false
  settle?.(ok)
  settle = null
}

/** Saves the settings when they changed. Returns false when they were not saved (invalid, declined, conflict, failure). */
async function save(): Promise<boolean> {
  const saved = record.value
  if (!saved) return true
  const next = current.value
  const change = ttftRuleChange(saved, next)
  // Untouched settings send nothing, so they never hold up the page's save.
  if (change === 'none') return true
  if (!isValid.value) return false
  if ((change === 'relax' || change === 'mixed') && !(await confirmRelax())) {
    declined.value = true
    return false
  }
  declined.value = false
  saveFailed.value = false
  saveMessage.value = ''
  saving.value = true
  try {
    const result = await schedQualityAPI.updateTTFTRule({ expected_version: saved.version, ...next })
    // Merged: an answer without the defaults or the replayed ranges keeps the ones loaded.
    const merged = { ...saved, ...result }
    record.value = merged
    fill(merged)
    conflict.value = false
    if (result.change === 'relax' || result.change === 'mixed') {
      appStore.showInfo(t('admin.schedQuality.ttftRule.savedRelaxed', { n: result.cleared ?? 0 }), 6000)
    }
    return true
  } catch (error) {
    if ((error as { status?: number })?.status === 409) conflict.value = true
    else {
      saveFailed.value = true
      saveMessage.value = extractApiErrorMessage(error, '')
    }
    return false
  } finally {
    saving.value = false
  }
}

/** Puts focus on the first invalid input (SettingsView, before saving the page). */
function focusInvalid() {
  const field = FIELDS.find((name) => !valid.value[name])
  if (field) document.getElementById(ids[field])?.focus()
}

/** The last save was refused (conflict or another failure), as opposed to declined or not needed. */
const failed = computed(() => conflict.value || saveFailed.value)

defineExpose({ save, valid: isValid, failed, unsaved, focusInvalid })
</script>
