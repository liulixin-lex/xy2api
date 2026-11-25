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
      <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <input :id="ids.ttft" v-model="form.ttft" type="number" step="0.1" min="0" :aria-invalid="!valid.ttft" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.ttft }]" @input="edited.ttft = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.seconds') }}</span>
        <span v-if="relaxed.has('ttfo')" class="text-xs text-amber-700 dark:text-amber-400" data-sq-relaxed="ttfo">{{ t('admin.schedQuality.policy.trialRelaxed') }}</span>
      </span>
      <!-- Under the limit, what it would let through: each model's accounts that reach it of those with enough samples. -->
      <div :class="reachClass" role="status" aria-live="polite" data-sq-reach="ttfo">
        <template v-for="item in reach.ttfo" :key="item.model"><span class="inline-flex items-baseline gap-1.5" :data-sq-reach-model="item.model"><span class="text-gray-500 dark:text-gray-400">{{ item.model }}</span><span :class="['sq-num', REACH_TONES[item.tone]]">{{ item.text }}</span></span></template>
        <span v-if="reach.state === 'failed'" class="sq-fail-word" data-sq-reach-failed>{{ t('admin.schedQuality.result.failed') }}</span>
        <span v-else-if="reach.state === 'waiting'" class="skeleton inline-block h-3 w-40 align-middle" />
        <span v-else-if="reach.state === 'none'" class="text-gray-400 dark:text-gray-500">{{ DASH }}</span>
      </div>

      <label :for="ids.error" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.error') }}</label>
      <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <input :id="ids.error" v-model="form.errorPct" type="number" step="0.1" :min="MIN_ERROR_PCT" max="100" :aria-invalid="!valid.error" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.error }]" @input="edited.error = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
        <span v-if="relaxed.has('error')" class="text-xs text-amber-700 dark:text-amber-400" data-sq-relaxed="error">{{ t('admin.schedQuality.policy.trialRelaxed') }}</span>
      </span>
      <!-- What the saved limit takes to judge the line bad (the backend's own numbers, by the rule's function). -->
      <p v-if="errorTrip" class="col-start-2 -mt-2 max-w-[520px] text-xs text-gray-500 dark:text-gray-400" data-sq-policy-notes="error">{{ errorTrip }}</p>

      <label :for="ids.cache" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.cache') }}</label>
      <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <input :id="ids.cache" v-model="form.cachePct" type="number" step="1" min="0" max="100" :aria-invalid="!valid.cache" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.cache }]" @input="edited.cache = true" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
        <span v-if="relaxed.has('cache')" class="text-xs text-amber-700 dark:text-amber-400" data-sq-relaxed="cache">{{ t('admin.schedQuality.policy.trialRelaxed') }}</span>
      </span>
      <div :class="reachClass" role="status" aria-live="polite" data-sq-reach="cache">
        <template v-for="item in reach.cache" :key="item.model"><span class="inline-flex items-baseline gap-1.5" :data-sq-reach-model="item.model"><span class="text-gray-500 dark:text-gray-400">{{ item.model }}</span><span :class="['sq-num', REACH_TONES[item.tone]]">{{ item.text }}</span></span></template>
        <span v-if="reach.state === 'failed'" class="sq-fail-word" data-sq-reach-failed>{{ t('admin.schedQuality.result.failed') }}</span>
        <span v-else-if="reach.state === 'waiting'" class="skeleton inline-block h-3 w-40 align-middle" />
        <span v-else-if="reach.state === 'none'" class="text-gray-400 dark:text-gray-500">{{ DASH }}</span>
      </div>
      <p v-if="cacheHigh" class="col-start-2 -mt-2 max-w-[520px] text-xs text-amber-700 dark:text-amber-400" data-sq-policy-notes="cache">{{ t('admin.schedQuality.policy.cacheHigh') }}</p>

      <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.takeover') }}</span>
      <Select v-model="form.takeover" :options="takeoverOptions" :searchable="false" :aria-label="t('admin.schedQuality.policy.takeover')" class="w-40" />
      <p class="col-start-2 max-w-[520px] text-xs text-gray-500 dark:text-gray-400" data-sq-policy-notes="trial">{{ t('admin.schedQuality.policy.trialStable') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onScopeDispose, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendDim, BackendGroupPolicy, BackendPolicy, BackendPolicyPreview, BackendPolicyReach, SchedTakeoverIdleSeconds } from '@/api/admin/schedQualityTypes'
import { useAppStore } from '@/stores/app'
import { DASH } from '@/utils/schedQualityFormat'

// Quality gate block of the OpenAI group edit dialog (design spec D.1, D.2). Saved on its own with the policy
// version; a 409 shows "失败 + 重新载入", any other error "失败".
// Under the first-output limit and the cache minimum, each model's accounts that reach the value in the form, of those
// with enough samples over the last 12 hours (the server's preview, as the values change); beside a field whose new
// value saving would relax, that saving clears and judges the line again. After a save that relaxed lines, a toast says
// how many line states were cleared.
// The ranges below are the ones the backend accepts now: first output any positive number, errors from 1 %.
const TAKEOVER_VALUES: SchedTakeoverIdleSeconds[] = [-1, 0, 300, 900, 1800]
/** Typing pauses this long before the preview is asked for. */
const PREVIEW_DELAY_MS = 400
/** The backend rejects an error limit below 1 % (SCHED_ERROR_RATE_TOO_LOW). */
const MIN_ERROR_PCT = 1

const props = defineProps<{ groupId: number }>()
const { t } = useI18n()
const appStore = useAppStore()
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

// The saved limit in force and what it takes to judge the error line bad once (50 and 100 clear outcomes): the
// backend's numbers, never worked out here. The backend lists only the examples that can trip; with a high limit there
// may be one or none, and none says the line is not judged bad within 100. Older answers without the fields show nothing.
const errorTrip = computed(() => {
  const saved = record.value
  const examples = saved?.error_trip_examples
  if (!saved || typeof saved.effective_max_error_rate !== 'number' || !Array.isArray(examples)) return ''
  const limit = `${pct(saved.effective_max_error_rate)}%`
  if (!examples.length) return t('admin.schedQuality.policy.errorTripNone', { limit })
  return t('admin.schedQuality.policy.errorTrip', {
    limit,
    examples: examples
      .map((item) => t('admin.schedQuality.policy.errorTripItem', { known: item.known, failed: item.failed }))
      .join(t('admin.schedQuality.policy.errorTripJoiner'))
  })
})
/**
 * A saved cache floor above 85 %: accounts go in and out around the line more (an observed relation). By the saved
 * policy like the error examples (review item 8): it follows a successful save or a reload, not the typing.
 */
const CACHE_HIGH = 0.85
const cacheHigh = computed(() => (record.value?.policy.min_cache_ratio ?? 0) > CACHE_HIGH)

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

// ---- What the thresholds in the form would let through, asked of the server ----
// From the moment the block opens (the saved thresholds first) and after each typing pause, the preview answers, per
// model, how many of the group's accounts with enough samples reach the first-output limit and the cache minimum in the
// form (reach), and which lines saving would relax. Only the latest request counts: a new one aborts the one before,
// and an answer that is no longer the latest is dropped. While a field is invalid nothing is asked and the last answer
// stays dimmed.
const preview = reactive({
  state: 'idle' as 'idle' | 'loading' | 'loaded' | 'failed',
  value: null as BackendPolicyPreview | null,
  /** What the answer shown was asked for: the group, the saved version and the policy in the form then. */
  key: ''
})
const previewKey = (groupId: number, version: number, policy: BackendPolicy) => JSON.stringify([groupId, version, policy])
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
    preview.key = previewKey(groupId, version, policy)
    preview.state = 'loaded'
  } catch {
    if (seq !== previewSeq) return
    preview.state = 'failed'
  } finally {
    if (previewController === controller) previewController = null
  }
}
watch(() => props.groupId, () => {
  stopPreview()
  Object.assign(preview, { state: 'idle', value: null, key: '' })
})
watch([isValid, current], ([ok]) => {
  stopPreview()
  if (!record.value) return
  if (!ok) {
    if (preview.state === 'loading') preview.state = preview.value ? 'loaded' : 'idle'
    return
  }
  const { version } = record.value
  const groupId = props.groupId
  const policy = { ...current.value }
  previewTimer = setTimeout(() => { previewTimer = null; void runPreview(groupId, version, policy) }, PREVIEW_DELAY_MS)
})
onScopeDispose(stopPreview)

/**
 * The answer shown is for the form as it is now: asked for these very values on this saved version, and nothing newer
 * since (no edit waiting for its pause, no request out, no failure).
 */
const fresh = computed(() =>
  preview.state === 'loaded' && !!record.value && preview.key === previewKey(props.groupId, record.value.version, current.value)
)
/**
 * Lines saving the form would relax (cleared and judged again at once): only from an answer for the values in the form
 * now and while they differ from the saved. While a newer answer is awaited, or it failed, none is shown: a wrong
 * consequence of saving is worse than none.
 */
const relaxed = computed(() => new Set<BackendDim>(fresh.value && changed.value ? preview.value?.relaxed_lines ?? [] : []))

type ReachTone = 'ok' | 'low' | 'none'
const REACH_TONES: Record<ReachTone, string> = {
  ok: 'text-gray-700 dark:text-gray-300',
  low: 'text-amber-700 dark:text-amber-400',
  none: 'text-gray-400 dark:text-gray-500'
}
/**
 * Each model's count under its input: "5 / 8", amber when fewer than half reach it; a model without any account with
 * enough samples reads a grey dash, never "0 / 0". The state says what shows when there are no counts yet: a skeleton
 * before the first answer, 失败 when the latest failed, a dash when the answer has none (the 12-hour figures still
 * loading, no member with traffic, or an older backend).
 */
const reach = computed(() => {
  const items = preview.value?.reach ?? []
  const of = (count: (item: BackendPolicyReach) => { ok: number; n: number }) =>
    items.map((item) => {
      const { ok, n } = count(item)
      const tone: ReachTone = n > 0 ? (ok * 2 < n ? 'low' : 'ok') : 'none'
      return { model: item.model, text: n > 0 ? `${ok} / ${n}` : DASH, tone }
    })
  const state = preview.state === 'failed' ? 'failed' : !preview.value ? 'waiting' : items.length ? 'shown' : 'none'
  return { state, ttfo: state === 'shown' ? of((item) => item.first_output) : [], cache: state === 'shown' ? of((item) => item.cache) : [] }
})
/** The row under an input: wrapped items in the input's column, dimmed whenever they are not for the values in the form. */
const reachClass = computed(() => [
  'col-start-2 -mt-2 flex max-w-[520px] flex-wrap gap-x-4 gap-y-1 text-xs',
  { 'opacity-60': preview.value !== null && !fresh.value }
])

/** Saves the policy when it changed. Returns false when it was not saved (invalid, conflict, failure). */
async function save(): Promise<boolean> {
  if (!record.value) return !loadFailed.value
  // An untouched block sends nothing, so it never holds up the group dialog.
  if (!changed.value) return true
  if (!isValid.value) return false
  saveFailed.value = false
  try {
    const saved = record.value
    const result = await schedQualityAPI.updateGroupPolicy(props.groupId, { expected_version: saved.version, policy: current.value })
    // Merged: an answer without some field keeps what was there. The answer carries the saved limit's examples.
    record.value = { ...saved, ...result, policy: { ...saved.policy, ...result?.policy } }
    conflict.value = false
    const relaxed = result?.relaxed_lines ?? []
    if (relaxed.length) {
      appStore.showInfo(t('admin.schedQuality.policy.savedRelaxed', {
        lines: relaxed.map((line) => t(`admin.schedQuality.lineName.${line}`)).join(t('admin.schedQuality.evidence.joiner')),
        n: result?.cleared ?? 0
      }), 6000)
    }
    return true
  } catch (error) {
    if ((error as { status?: number })?.status === 409) conflict.value = true
    else saveFailed.value = true
    return false
  }
}

defineExpose({ save, valid: isValid })
</script>
