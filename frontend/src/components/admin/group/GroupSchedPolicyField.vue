<template>
  <section class="border-t border-gray-200 pt-4 dark:border-dark-700" data-sq-policy>
    <div v-if="conflict || saveFailed" class="mb-4 flex items-center gap-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 dark:border-red-500/30 dark:bg-red-500/10" role="status" data-sq-policy-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button v-if="conflict" type="button" class="btn btn-secondary btn-sm" data-sq-policy-reload @click="reload">{{ t('admin.schedQuality.action.reload') }}</button>
    </div>
    <div v-if="loadFailed" class="flex items-center gap-3">
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="reload">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <div v-else-if="loaded" class="grid grid-cols-[88px_minmax(0,1fr)] items-center gap-x-4 gap-y-4 text-sm">
      <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.gate') }}</span>
      <Toggle v-model="form.enabled" :aria-label="t('admin.schedQuality.policy.gate')" />

      <label :for="ids.ttft" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.ttft') }}</label>
      <span class="inline-flex items-center gap-2">
        <input :id="ids.ttft" v-model="form.ttft" type="number" step="0.1" min="0" :aria-invalid="!valid.ttft" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.ttft }]" @input="edited.ttft = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.seconds') }}</span>
      </span>

      <label :for="ids.error" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.error') }}</label>
      <span class="inline-flex items-center gap-2">
        <input :id="ids.error" v-model="form.errorPct" type="number" step="0.1" :min="MIN_ERROR_PCT" max="100" :aria-invalid="!valid.error" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.error }]" @input="edited.error = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
      </span>

      <label :for="ids.cache" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.cache') }}</label>
      <span class="inline-flex items-center gap-2">
        <input :id="ids.cache" v-model="form.cachePct" type="number" step="1" min="0" max="100" :aria-invalid="!valid.cache" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.cache }]" @input="edited.cache = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
      </span>

      <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.takeover') }}</span>
      <Select v-model="form.takeover" :options="takeoverOptions" :searchable="false" :aria-label="t('admin.schedQuality.policy.takeover')" class="w-40" />

      <template v-if="previewShown">
        <span class="self-start pt-0.5 text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.trial') }}</span>
        <div class="min-w-0" role="status" aria-live="polite" data-sq-trial>
          <span v-if="preview.state === 'failed'" class="sq-fail-word text-sm" data-sq-trial-failed>{{ t('admin.schedQuality.result.failed') }}</span>
          <span v-else-if="!preview.value" class="skeleton inline-block h-3.5 w-40 align-middle" />
          <template v-else>
            <div :class="['flex flex-wrap gap-x-4 gap-y-1 text-[13px]', { 'opacity-60': preview.state === 'loading' }]">
              <span v-for="item in previewCounts" :key="item.kind" class="inline-flex items-center gap-1.5" :data-sq-trial="item.kind">
                <span :class="['sq-shape', item.kind === 'normal' ? 'sq-shape--pass' : 'sq-shape--fail']" aria-hidden="true" />
                <span class="sq-num text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.trialCount', { state: t(`admin.schedQuality.state.${item.kind}`), now: item.now, after: item.after }) }}</span>
              </span>
            </div>
            <div v-if="preview.value.changed.length" class="mt-2 flex max-w-[520px] flex-wrap gap-x-4 gap-y-1 text-[13px] text-gray-700 dark:text-gray-300" data-sq-trial-names>
              <span v-for="item in shownChanged" :key="`${item.account_id}:${item.model}`" v-tip="item.model">{{ item.name }}</span>
              <span v-if="preview.value.changed.length > shownChanged.length" class="sq-text-3">{{ t('admin.schedQuality.more', { n: preview.value.changed.length - shownChanged.length }) }}</span>
            </div>
          </template>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onScopeDispose, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendGroupPolicy, BackendPolicy, BackendPolicyPreview, SchedTakeoverIdleSeconds } from '@/api/admin/schedQualityTypes'
import { vTip } from '@/utils/tip'

// Quality gate block of the OpenAI group edit dialog (design spec D.1, D.2). Saved on its own with the policy
// version; a 409 shows "失败 + 重新载入", any other error "失败".
// The per-class limits of v0.6.4 are gone with the request classes. The trial no longer recomputes verdicts in the
// browser from the board's numbers (the backend dropped them in 0.6.5): while the thresholds differ from the saved
// ones, it asks the server preview what they would do, and shows the counts now → after and the accounts that turn.
// The ranges below are the ones the backend accepts now: first output any positive number, errors from 5 %.
const TAKEOVER_VALUES: SchedTakeoverIdleSeconds[] = [-1, 0, 300, 900, 1800]
/** Typing pauses this long before the preview is asked for. */
const PREVIEW_DELAY_MS = 400
const PREVIEW_NAMES = 12
/** The backend rejects an error limit below 5 % (SCHED_ERROR_RATE_TOO_LOW). */
const MIN_ERROR_PCT = 5

const props = defineProps<{ groupId: number }>()
const { t } = useI18n()
const ids = { ttft: `sq-ttft-${useId()}`, error: `sq-err-${useId()}`, cache: `sq-cache-${useId()}` }

const record = shallowRef<BackendGroupPolicy | null>(null)
const loaded = computed(() => record.value !== null)
const loadFailed = ref(false)
const conflict = ref(false)
const saveFailed = ref(false)
const form = reactive({ enabled: false, ttft: '10', errorPct: '5', cachePct: '90', takeover: 0 as SchedTakeoverIdleSeconds })
// The inputs show rounded values; a field the user has not typed in keeps the API value as is.
const edited = reactive({ ttft: false, error: false, cache: false })

const pct = (ratio: number) => String(+(ratio * 100).toFixed(2))
function fillForm(policy: BackendPolicy) {
  Object.assign(edited, { ttft: false, error: false, cache: false })
  Object.assign(form, {
    enabled: policy.enabled,
    ttft: String(policy.ttft_seconds),
    errorPct: pct(policy.max_error_rate),
    cachePct: String(Math.round(policy.min_cache_ratio * 100)),
    takeover: policy.takeover_idle_seconds
  })
}

async function reload() {
  loadFailed.value = false
  try {
    const value = await schedQualityAPI.getGroupPolicy(props.groupId)
    record.value = value
    conflict.value = false
    saveFailed.value = false
    fillForm(value.policy)
  } catch {
    loadFailed.value = true
  }
}
watch(() => props.groupId, reload, { immediate: true })

const takeoverOptions = computed(() => TAKEOVER_VALUES.map((value) => ({
  value,
  label: value === -1
    ? t('admin.schedQuality.policy.takeoverOff')
    : value === 0 ? t('admin.schedQuality.policy.takeoverNow') : t('admin.schedQuality.policy.takeoverMin', { n: value / 60 })
})))

// Range checks: an invalid field gets the red frame and aria-invalid, no text.
const num = (value: string | number) => (String(value).trim() === '' ? NaN : Number(value))
const inRange = (value: number, min: number, max: number) => Number.isFinite(value) && value >= min && value <= max
const valid = computed(() => ({
  ttft: num(form.ttft) > 0 && Number.isFinite(num(form.ttft)),
  error: inRange(num(form.errorPct), MIN_ERROR_PCT, 100),
  cache: inRange(num(form.cachePct), 0, 100)
}))
const isValid = computed(() => valid.value.ttft && valid.value.error && valid.value.cache)

const current = computed<BackendPolicy>(() => {
  const saved = record.value?.policy
  return {
    enabled: form.enabled,
    ttft_seconds: saved && !edited.ttft ? saved.ttft_seconds : num(form.ttft),
    max_error_rate: saved && !edited.error ? saved.max_error_rate : Number((num(form.errorPct) / 100).toFixed(6)),
    min_cache_ratio: saved && !edited.cache ? saved.min_cache_ratio : Number((num(form.cachePct) / 100).toFixed(6)),
    takeover_idle_seconds: form.takeover
  }
})
const changed = computed(() => {
  const saved = record.value?.policy
  const value = current.value
  return !!saved && (saved.enabled !== value.enabled || saved.ttft_seconds !== value.ttft_seconds ||
    saved.max_error_rate !== value.max_error_rate || saved.min_cache_ratio !== value.min_cache_ratio ||
    saved.takeover_idle_seconds !== value.takeover_idle_seconds)
})

// ---- Server preview of unsaved thresholds ----
const preview = reactive({ state: 'idle' as 'idle' | 'loading' | 'loaded' | 'failed', value: null as BackendPolicyPreview | null })
const previewShown = computed(() => changed.value && isValid.value)
let previewTimer: ReturnType<typeof setTimeout> | null = null
let previewController: AbortController | null = null
let previewSeq = 0
function stopPreview() {
  previewSeq++
  if (previewTimer) clearTimeout(previewTimer)
  previewTimer = null
  previewController?.abort()
  previewController = null
}
async function runPreview(groupId: number, version: number, policy: BackendPolicy) {
  const seq = ++previewSeq
  const controller = new AbortController()
  previewController = controller
  preview.state = 'loading'
  try {
    const value = await schedQualityAPI.previewGroupPolicy(groupId, { expected_version: version, policy }, { signal: controller.signal })
    if (seq !== previewSeq) return
    preview.value = value
    preview.state = 'loaded'
  } catch {
    if (seq !== previewSeq) return
    preview.state = 'failed'
  } finally {
    if (previewController === controller) previewController = null
  }
}
watch([previewShown, current], ([shown]) => {
  stopPreview()
  if (!shown || !record.value) {
    Object.assign(preview, { state: 'idle', value: null })
    return
  }
  const { version } = record.value
  const groupId = props.groupId
  const policy = { ...current.value }
  previewTimer = setTimeout(() => { previewTimer = null; void runPreview(groupId, version, policy) }, PREVIEW_DELAY_MS)
})
onScopeDispose(stopPreview)
const previewCounts = computed(() => {
  const value = preview.value
  if (!value) return []
  return [
    { kind: 'normal' as const, now: value.now.normal, after: value.after.normal },
    { kind: 'cooling' as const, now: value.now.below, after: value.after.below }
  ]
})
const shownChanged = computed(() => preview.value?.changed.slice(0, PREVIEW_NAMES) ?? [])

/** Saves the policy when it changed. Returns false when it was not saved (invalid, conflict, failure). */
async function save(): Promise<boolean> {
  if (!record.value) return !loadFailed.value
  // An untouched block sends nothing, so it never holds up the group dialog.
  if (!changed.value) return true
  if (!isValid.value) return false
  saveFailed.value = false
  try {
    record.value = await schedQualityAPI.updateGroupPolicy(props.groupId, { expected_version: record.value.version, policy: current.value })
    conflict.value = false
    return true
  } catch (error) {
    if ((error as { status?: number })?.status === 409) conflict.value = true
    else saveFailed.value = true
    return false
  }
}

defineExpose({ save, valid: isValid })
</script>
