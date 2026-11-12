<template>
  <section class="border-t border-gray-200 pt-4 dark:border-dark-700" data-sq-policy>
    <div v-if="conflict" class="mb-4 flex items-center gap-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 dark:border-red-500/30 dark:bg-red-500/10" role="status">
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" data-sq-policy-reload @click="reload">{{ t('admin.schedQuality.action.reload') }}</button>
    </div>
    <div v-if="loadFailed" class="flex items-center gap-3">
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="reload">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <div v-else-if="loaded" class="grid grid-cols-[88px_minmax(0,1fr)] items-center gap-x-4 gap-y-4 text-sm">
      <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.gate') }}</span>
      <div class="flex min-w-0 items-center justify-between gap-3">
        <Toggle v-model="form.enabled" :aria-label="t('admin.schedQuality.policy.gate')" />
        <Select
          v-if="modelOptions.length"
          v-model="model"
          :options="modelOptions"
          :searchable="false"
          :aria-label="t('admin.schedQuality.aria.model')"
          class="sq-select-sm w-[150px]"
        />
      </div>

      <label :for="ids.ttft" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.ttft') }}</label>
      <div class="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2">
        <span class="inline-flex items-center gap-2">
          <input :id="ids.ttft" v-model="form.ttft" type="number" step="0.1" min="0.5" max="600" :aria-invalid="!valid.ttft" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.ttft }]" />
          <span class="sq-text-3">{{ t('admin.schedQuality.policy.seconds') }}</span>
        </span>
        <span class="inline-flex flex-wrap gap-x-3 text-[13px]" data-sq-class-limits>
          <span v-for="item in converted" :key="item.cls">
            <span class="sq-text-3">{{ t(`admin.schedQuality.class.${item.cls}`) }}</span> <b class="sq-num font-medium text-gray-700 dark:text-gray-300">{{ item.value }}s</b>
          </span>
        </span>
      </div>

      <label :for="ids.error" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.error') }}</label>
      <span class="inline-flex items-center gap-2">
        <input :id="ids.error" v-model="form.errorPct" type="number" step="0.1" min="0" max="100" :aria-invalid="!valid.error" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.error }]" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
      </span>

      <label :for="ids.cache" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.cache') }}</label>
      <span class="inline-flex items-center gap-2">
        <input :id="ids.cache" v-model="form.cachePct" type="number" step="1" min="0" max="100" :aria-invalid="!valid.cache" :class="['input w-24 py-1.5 text-right sq-num', { 'input-error': !valid.cache }]" />
        <span class="sq-text-3">{{ t('admin.schedQuality.policy.percent') }}</span>
      </span>

      <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.takeover') }}</span>
      <Select v-model="form.takeover" :options="takeoverOptions" :searchable="false" :aria-label="t('admin.schedQuality.policy.takeover')" class="w-40" />

      <span class="self-start pt-0.5 text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.policy.trial') }}</span>
      <div class="min-w-0" data-sq-trial>
        <div v-if="trial.total" class="sq-trial-bar">
          <span
            v-for="seg in trialSegments"
            :key="seg.kind"
            tabindex="-1"
            :aria-label="t('admin.schedQuality.aria.trial', { verdict: t(`admin.schedQuality.aria.${seg.kind}`), n: seg.count })"
            :class="['sq-trial-seg', `sq-trial-seg--${seg.kind}`]"
            :style="{ flexGrow: seg.count }"
            @click="toggleOpen(seg.kind)"
          />
        </div>
        <div class="mt-2.5 flex gap-2">
          <button
            v-for="kind in TRIAL_KINDS"
            :key="kind"
            type="button"
            :aria-expanded="openKind === kind"
            :aria-controls="ids.names"
            :aria-label="t('admin.schedQuality.aria.trial', { verdict: t(`admin.schedQuality.aria.${kind}`), n: trial[kind].length })"
            class="sq-focus inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-sm font-medium text-gray-900 hover:bg-gray-100 dark:text-gray-100 dark:hover:bg-dark-700"
            :data-sq-trial="kind"
            @click="toggleOpen(kind)"
          >
            <span :class="['sq-shape', `sq-shape--${kind === 'pass' ? 'pass' : kind === 'fail' ? 'fail' : 'nodata'}`]" aria-hidden="true" />
            <span class="sq-num">{{ trial[kind].length }}</span>
          </button>
        </div>
        <div v-show="openKind" :id="ids.names" class="mt-2 flex max-w-[520px] flex-wrap gap-x-4 gap-y-1 text-[13px] text-gray-700 dark:text-gray-300" data-sq-trial-names>
          <span v-for="name in openKind ? trial[openKind] : []" :key="name">{{ name }}</span>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedBoard, SchedGroupPolicy, SchedPolicy, SchedRequestClass, SchedTakeoverIdleSeconds } from '@/types/schedQuality'
import { trialVerdicts } from '@/utils/schedQualityVerdict'

// Quality gate block of the OpenAI group edit dialog (design spec D.1, D.2). Saved on its own with the policy
// version; a 409 shows "失败 + 重新载入". The trial recomputes the verdicts of the group's accounts from the board's
// evidence numbers on every input change, without requests; verdicts under saved thresholds always come from the API.
type TrialKind = 'pass' | 'fail' | 'nodata'
const TRIAL_KINDS: TrialKind[] = ['pass', 'fail', 'nodata']
const CONVERTED_CLASSES: SchedRequestClass[] = ['light', 'heavy', 'xheavy']
const TAKEOVER_VALUES: SchedTakeoverIdleSeconds[] = [-1, 0, 300, 900, 1800]

const props = defineProps<{ groupId: number }>()
const { t } = useI18n()
const ids = { ttft: `sq-ttft-${useId()}`, error: `sq-err-${useId()}`, cache: `sq-cache-${useId()}`, names: `sq-names-${useId()}` }

const record = shallowRef<SchedGroupPolicy | null>(null)
const loaded = computed(() => record.value !== null)
const loadFailed = ref(false)
const conflict = ref(false)
const form = reactive({ enabled: false, ttft: '10', errorPct: '5', cachePct: '90', takeover: 0 as SchedTakeoverIdleSeconds })
const model = ref<string>('')

const pct = (ratio: number) => String(+(ratio * 100).toFixed(2))
function fillForm(policy: SchedPolicy) {
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
    fillForm(value.policy)
    const models = Object.keys(value.class_limits_ms).filter((key) => key !== 'default')
    if (!models.includes(model.value)) model.value = models[0] ?? ''
  } catch {
    loadFailed.value = true
  }
}
watch(() => props.groupId, reload, { immediate: true })

const modelOptions = computed(() =>
  Object.keys(record.value?.class_limits_ms ?? {}).filter((key) => key !== 'default').map((key) => ({ value: key, label: key }))
)
const takeoverOptions = computed(() => TAKEOVER_VALUES.map((value) => ({
  value,
  label: value === -1
    ? t('admin.schedQuality.policy.takeoverOff')
    : value === 0 ? t('admin.schedQuality.policy.takeoverNow') : t('admin.schedQuality.policy.takeoverMin', { n: value / 60 })
})))

// Range checks: an invalid field gets the red frame and aria-invalid, no text; the trial keeps the last valid value.
const num = (value: string | number) => (String(value).trim() === '' ? NaN : Number(value))
const inRange = (value: number, min: number, max: number) => Number.isFinite(value) && value >= min && value <= max
const valid = computed(() => ({
  ttft: inRange(num(form.ttft), 0.5, 600),
  error: inRange(num(form.errorPct), 0, 100),
  cache: inRange(num(form.cachePct), 0, 100)
}))
const isValid = computed(() => valid.value.ttft && valid.value.error && valid.value.cache)

const current = computed<SchedPolicy>(() => ({
  enabled: form.enabled,
  ttft_seconds: num(form.ttft),
  max_error_rate: Number((num(form.errorPct) / 100).toFixed(6)),
  min_cache_ratio: Number((num(form.cachePct) / 100).toFixed(6)),
  takeover_idle_seconds: form.takeover
}))
const lastValid = ref<SchedPolicy | null>(null)
watch(current, (value) => { if (isValid.value) lastValid.value = value }, { immediate: true })

// Converted limits for the other classes: X × (class limit / regular limit) of the selected model.
const converted = computed(() => {
  const limits = record.value?.class_limits_ms[model.value] ?? record.value?.class_limits_ms.default
  const x = lastValid.value?.ttft_seconds
  if (!limits || !x || !limits.regular) return []
  return CONVERTED_CLASSES.map((cls) => ({ cls, value: (x * limits[cls] / limits.regular).toFixed(1) }))
})

// Trial on the board of the selected model.
const board = shallowRef<SchedBoard | null>(null)
watch(() => [props.groupId, model.value] as const, async ([id, m]) => {
  board.value = null
  if (!m) return
  try {
    const value = await schedQualityAPI.groupBoard(id, m)
    if (props.groupId === id && model.value === m) board.value = value
  } catch {
    // No trial without board data.
  }
}, { immediate: true })

const trial = computed(() => {
  const groups: Record<TrialKind, string[]> & { total: number } = { pass: [], fail: [], nodata: [], total: 0 }
  const thresholds = lastValid.value
  if (!board.value || !thresholds) return groups
  for (const account of board.value.accounts) {
    const v = trialVerdicts(thresholds, account)
    const all = [v.ttfo, v.error, v.cache]
    const kind: TrialKind = all.includes('fail') ? 'fail' : all.every((x) => x === 'nodata') ? 'nodata' : 'pass'
    groups[kind].push(account.name)
    groups.total++
  }
  return groups
})
const trialSegments = computed(() => TRIAL_KINDS.map((kind) => ({ kind, count: trial.value[kind].length })).filter((seg) => seg.count > 0))
const openKind = ref<TrialKind | null>(null)
const toggleOpen = (kind: TrialKind) => { openKind.value = openKind.value === kind ? null : kind }

const changed = computed(() => {
  const saved = record.value?.policy
  const value = current.value
  return !!saved && (saved.enabled !== value.enabled || saved.ttft_seconds !== value.ttft_seconds ||
    saved.max_error_rate !== value.max_error_rate || saved.min_cache_ratio !== value.min_cache_ratio ||
    saved.takeover_idle_seconds !== value.takeover_idle_seconds)
})

/** Saves the policy when it changed. Returns false when it was not saved (invalid, conflict, failure). */
async function save(): Promise<boolean> {
  if (!record.value) return !loadFailed.value
  if (!isValid.value) return false
  if (!changed.value) return true
  try {
    record.value = await schedQualityAPI.updateGroupPolicy(props.groupId, { expected_version: record.value.version, policy: current.value })
    conflict.value = false
    return true
  } catch (error) {
    if ((error as { status?: number })?.status === 409) conflict.value = true
    return false
  }
}

defineExpose({ save, valid: isValid })
</script>
