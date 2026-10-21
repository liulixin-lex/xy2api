<template>
  <section class="card p-6" data-testid="gateway-resilience-settings" aria-labelledby="gateway-resilience-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h2 id="gateway-resilience-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.resilience.settings.title') }}</h2>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.resilience.settings.hint') }}</p>
      </div>
      <RouterLink to="/admin/ops" class="btn btn-secondary">{{ t('admin.resilience.settings.openPanel') }}</RouterLink>
    </div>
    <div v-if="loading && !loaded" class="mt-5 h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" role="status" :aria-label="t('common.loading')" />
    <div v-if="loadFailed" class="mt-4 flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.resilience.settings.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="load">{{ t('admin.resilience.settings.reload') }}</button>
    </div>
    <div v-if="loaded && draft" class="mt-5 space-y-5">
      <div class="flex items-start justify-between gap-4">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.resilience.settings.enabled') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.resilience.settings.enabledHint') }}</p>
        </div>
        <Toggle v-model="draft.enabled" :disabled="saving" :aria-label="t('admin.resilience.settings.enabled')" data-testid="resilience-enabled" />
      </div>

      <div class="grid gap-3 border-t border-gray-100 pt-5 lg:grid-cols-2 dark:border-dark-700">
        <div v-for="section in sections" :key="section.key" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" :data-testid="`resilience-card-${section.key}`">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <p class="font-medium text-gray-900 dark:text-white">{{ t(`admin.resilience.settings.sections.${section.key}`) }}</p>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t(`admin.resilience.settings.sections.${section.key}Hint`) }}</p>
            </div>
            <Toggle
              v-if="section.get"
              :model-value="section.get(draft)"
              :disabled="saving || !draft.enabled"
              :aria-label="t(`admin.resilience.settings.sections.${section.key}`)"
              :data-testid="`resilience-section-${section.key}`"
              @update:model-value="(v: boolean) => draft && section.set?.(draft, v)"
            />
          </div>
          <div v-if="fieldsOf(section.key).length || section.key === 'keepalive'" class="mt-3 grid gap-3 sm:grid-cols-2">
            <div v-for="field in fieldsOf(section.key)" :key="field.key">
              <label :for="`resilience-${field.key}`" class="input-label">{{ t(`admin.resilience.settings.fields.${field.key}`) }}</label>
              <input
                :id="`resilience-${field.key}`"
                :value="field.get(draft)"
                type="number"
                :min="field.min"
                :max="field.max"
                step="1"
                class="input"
                :disabled="saving"
                :data-testid="`resilience-field-${field.key}`"
                @input="onNumber(field, ($event.target as HTMLInputElement).value)"
              >
              <p v-if="field.hint" class="input-hint">{{ t(`admin.resilience.settings.fields.${field.key}Hint`) }}</p>
            </div>
            <div v-if="section.key === 'keepalive'" class="sm:col-span-2">
              <label for="resilience-edges" class="input-label">{{ t('admin.resilience.settings.fields.edges') }}</label>
              <input id="resilience-edges" v-model="edgesText" type="text" class="input" :disabled="saving" data-testid="resilience-field-edges">
              <p class="input-hint">{{ t('admin.resilience.settings.fields.edgesHint') }}</p>
            </div>
          </div>
          <ul v-if="switchesOf(section.key).length" class="mt-3 space-y-3">
            <li v-for="sw in switchesOf(section.key)" :key="sw.key" class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="text-sm text-gray-900 dark:text-white">{{ t(`admin.resilience.settings.switches.${sw.key}`) }}</p>
                <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ t(`admin.resilience.settings.switches.${sw.key}Hint`) }}</p>
              </div>
              <Toggle
                :model-value="sw.get(draft)"
                :disabled="saving || !draft.enabled || (section.get ? !section.get(draft) : false)"
                :aria-label="t(`admin.resilience.settings.switches.${sw.key}`)"
                :data-testid="`resilience-switch-${sw.key}`"
                @update:model-value="(v: boolean) => draft && sw.set(draft, v)"
              />
            </li>
          </ul>
        </div>
      </div>

      <details class="border-t border-gray-100 pt-5 dark:border-dark-700" @toggle="onAdvancedToggle">
        <summary class="cursor-pointer font-medium text-gray-900 dark:text-white">{{ t('admin.resilience.settings.advanced') }}</summary>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.resilience.settings.advancedHint') }}</p>
        <label for="resilience-json" class="sr-only">{{ t('admin.resilience.settings.advanced') }}</label>
        <textarea id="resilience-json" v-model="advancedText" rows="14" class="input mt-3 font-mono text-xs" :disabled="saving" spellcheck="false" data-testid="resilience-json" />
        <div class="mt-3 flex flex-wrap gap-3">
          <button type="button" class="btn btn-secondary" :disabled="saving" data-testid="resilience-apply-json" @click="applyAdvanced">{{ t('admin.resilience.settings.applyAdvanced') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="saving || !defaults" @click="resetDefaults">{{ t('admin.resilience.settings.resetDefaults') }}</button>
        </div>
      </details>

      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" :disabled="saving || loading || !dirty" data-testid="resilience-save" @click="save">{{ saving ? t('common.saving') : t('admin.resilience.settings.save') }}</button>
        <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
        <p v-else-if="saved && !dirty" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ t('admin.resilience.settings.saved') }}</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { adminAPI } from '@/api/admin'
import type { GatewayResilienceSettings } from '@/api/admin/resilience'
import Toggle from '@/components/common/Toggle.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

type Settings = GatewayResilienceSettings

// A card without get/set has no switch of its own (its switches still follow the master switch).
interface SectionDef {
  key: string
  get?: (s: Settings) => boolean
  set?: (s: Settings, v: boolean) => void
}

interface SwitchField {
  key: string
  section: string
  get: (s: Settings) => boolean
  set: (s: Settings, v: boolean) => void
}

interface NumberField {
  key: string
  section: string
  min: number
  max: number
  hint?: boolean
  get: (s: Settings) => number
  set: (s: Settings, v: number) => void
}

const { t } = useI18n()
const draft = ref<Settings | null>(null)
const defaults = ref<Settings | null>(null)
const baseline = ref('')
const advancedText = ref('')
const loading = ref(false), saving = ref(false), loaded = ref(false), loadFailed = ref(false), saved = ref(false)
const error = ref('')
let disposed = false

const sections: SectionDef[] = [
  { key: 'session', get: (s) => s.session.enabled, set: (s, v) => { s.session.enabled = v } },
  { key: 'lastResort', get: (s) => s.last_resort.enabled, set: (s, v) => { s.last_resort.enabled = v } },
  { key: 'balance', get: (s) => s.balance.enabled, set: (s, v) => { s.balance.enabled = v } },
  { key: 'budget', get: (s) => s.budget.enabled, set: (s, v) => { s.budget.enabled = v } },
  { key: 'failure', get: (s) => s.failure.decision_v2, set: (s, v) => { s.failure.decision_v2 = v } },
  { key: 'stream' },
  { key: 'keepalive', get: (s) => s.keepalive.enabled, set: (s, v) => { s.keepalive.enabled = v } },
  { key: 'stall', get: (s) => s.stall.enabled, set: (s, v) => { s.stall.enabled = v } },
  { key: 'disconnect', get: (s) => s.disconnect.enabled, set: (s, v) => { s.disconnect.enabled = v } },
  { key: 'errors', get: (s) => s.errors.enabled, set: (s, v) => { s.errors.enabled = v } },
  { key: 'capability', get: (s) => s.capability.stream_only_learning, set: (s, v) => { s.capability.stream_only_learning = v } },
  { key: 'fallbackGroup', get: (s) => s.fallback_group.enabled, set: (s, v) => { s.fallback_group.enabled = v } },
  { key: 'cacheAware', get: (s) => s.reservation.cache_aware_estimate, set: (s, v) => { s.reservation.cache_aware_estimate = v } },
  { key: 'outputP99', get: (s) => s.reservation.output_p99_cap, set: (s, v) => { s.reservation.output_p99_cap = v } }
]

// Ranges mirror resilience.Settings.Validate; the server stays authoritative.
const numberFields: NumberField[] = [
  { key: 'balancePause', section: 'balance', min: 30, max: 3600, get: (s) => s.balance.pause_seconds, set: (s, v) => { s.balance.pause_seconds = v } },
  { key: 'maxAttempts', section: 'budget', min: 1, max: 10, get: (s) => s.budget.max_attempts, set: (s, v) => { s.budget.max_attempts = v } },
  { key: 'preOutputBudget', section: 'budget', min: 60, max: 290, get: (s) => s.budget.pre_output_budget_seconds, set: (s, v) => { s.budget.pre_output_budget_seconds = v } },
  { key: 'keepaliveDelay', section: 'keepalive', min: 5, max: 60, get: (s) => s.keepalive.delay_seconds, set: (s, v) => { s.keepalive.delay_seconds = v } },
  { key: 'keepaliveInterval', section: 'keepalive', min: 5, max: 30, get: (s) => s.keepalive.interval_seconds, set: (s, v) => { s.keepalive.interval_seconds = v } },
  { key: 'textStall', section: 'stall', min: 30, max: 280, get: (s) => s.stall.text_stall_seconds, set: (s, v) => { s.stall.text_stall_seconds = v } },
  { key: 'partialDrain', section: 'disconnect', min: 1, max: 3600, get: (s) => s.disconnect.partial_drain_max_seconds, set: (s, v) => { s.disconnect.partial_drain_max_seconds = v } },
  { key: 'abuseThreshold', section: 'disconnect', min: 1, max: 100000, get: (s) => s.disconnect.abuse_guard.threshold_per_hour, set: (s, v) => { s.disconnect.abuse_guard.threshold_per_hour = v } },
  { key: 'negativeCache', section: 'errors', min: 1, max: 60, get: (s) => s.errors.negative_cache_seconds, set: (s, v) => { s.errors.negative_cache_seconds = v } },
  { key: 'codexRetryAfterCap', section: 'errors', min: 0, max: 30, hint: true, get: (s) => s.errors.codex_retry_after_cap_seconds, set: (s, v) => { s.errors.codex_retry_after_cap_seconds = v } },
  { key: 'learnTtl', section: 'capability', min: 600, max: 86400, hint: true, get: (s) => s.capability.learn_ttl_seconds, set: (s, v) => { s.capability.learn_ttl_seconds = v } },
  { key: 'returnStable', section: 'fallbackGroup', min: 1, max: 86400, get: (s) => s.fallback_group.return_stable_seconds, set: (s, v) => { s.fallback_group.return_stable_seconds = v } }
]

const switchFields: SwitchField[] = [
  { key: 'preludeStaging', section: 'stream', get: (s) => s.stream.prelude_staging, set: (s, v) => { s.stream.prelude_staging = v } },
  { key: 'nativePreludeWhitelist', section: 'stream', get: (s) => s.stream.native_prelude_whitelist, set: (s, v) => { s.stream.native_prelude_whitelist = v } },
  { key: 'strictTerminal', section: 'stream', get: (s) => s.stream.strict_terminal, set: (s, v) => { s.stream.strict_terminal = v } },
  { key: 'failedFrameRetryAfter', section: 'errors', get: (s) => s.errors.failed_frame_retry_after, set: (s, v) => { s.errors.failed_frame_retry_after = v } }
]

function fieldsOf(section: string): NumberField[] {
  return numberFields.filter((field) => field.section === section)
}

function switchesOf(section: string): SwitchField[] {
  return switchFields.filter((field) => field.section === section)
}

const dirty = computed(() => draft.value !== null && JSON.stringify(draft.value) !== baseline.value)

const edgesText = computed({
  get: () => draft.value?.keepalive.edges.join(', ') ?? '',
  set: (value: string) => {
    if (!draft.value) return
    draft.value.keepalive.edges = value.split(',').map((e) => e.trim().toLowerCase()).filter(Boolean)
  }
})

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function isSettings(value: unknown): value is Settings {
  if (!value || typeof value !== 'object') return false
  const s = value as Partial<Settings>
  return typeof s.enabled === 'boolean' && !!s.budget && !!s.keepalive && !!s.errors && !!s.disconnect &&
    !!s.fallback_group && !!s.reservation && !!s.session && !!s.last_resort && !!s.balance && !!s.stall &&
    !!s.failure && !!s.stream && !!s.capability
}

function accept(settings: Settings, defaultSettings?: Settings) {
  draft.value = clone(settings)
  baseline.value = JSON.stringify(draft.value)
  advancedText.value = JSON.stringify(draft.value, null, 2)
  if (defaultSettings) defaults.value = clone(defaultSettings)
}

function onNumber(field: NumberField, raw: string) {
  if (!draft.value) return
  const value = Number(raw)
  field.set(draft.value, Number.isFinite(value) ? value : 0)
}

function onAdvancedToggle(event: Event) {
  if ((event.target as HTMLDetailsElement).open && draft.value) {
    advancedText.value = JSON.stringify(draft.value, null, 2)
  }
}

function applyAdvanced() {
  error.value = ''
  try {
    const parsed: unknown = JSON.parse(advancedText.value)
    if (!isSettings(parsed)) throw new Error('shape')
    draft.value = clone(parsed)
  } catch {
    error.value = t('admin.resilience.settings.invalidJson')
  }
}

function resetDefaults() {
  if (!defaults.value) return
  draft.value = clone(defaults.value)
  advancedText.value = JSON.stringify(draft.value, null, 2)
}

function validate(settings: Settings): string {
  for (const field of numberFields) {
    const value = field.get(settings)
    if (!Number.isInteger(value) || value < field.min || value > field.max) {
      return t('admin.resilience.settings.invalidNumber', {
        field: t(`admin.resilience.settings.fields.${field.key}`), min: field.min, max: field.max
      })
    }
  }
  return ''
}

async function load() {
  if (loading.value || saving.value) return
  loading.value = true; loadFailed.value = false
  try {
    const result = await adminAPI.resilience.getSettings()
    if (disposed) return
    if (!isSettings(result?.settings)) throw new Error('unexpected gateway resilience settings')
    accept(result.settings, result.defaults)
    loaded.value = true
  } catch {
    if (!disposed) loadFailed.value = true
  } finally {
    if (!disposed) loading.value = false
  }
}

async function save() {
  if (saving.value || loading.value || !dirty.value || !draft.value) return
  error.value = ''; saved.value = false
  const invalid = validate(draft.value)
  if (invalid) {
    error.value = invalid
    return
  }
  saving.value = true
  try {
    const result = await adminAPI.resilience.updateSettings(clone(draft.value))
    if (disposed) return
    if (!isSettings(result?.settings)) throw new Error('unexpected gateway resilience settings')
    accept(result.settings, result.defaults)
    saved.value = true
  } catch (cause) {
    if (!disposed) error.value = extractApiErrorMessage(cause, t('admin.resilience.settings.saveFailed'))
  } finally {
    if (!disposed) saving.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>
