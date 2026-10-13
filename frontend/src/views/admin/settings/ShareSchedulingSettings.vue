<template>
  <section class="card p-6" data-testid="share-scheduling-settings" aria-labelledby="share-scheduling-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h2 id="share-scheduling-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.settings.title') }}</h2>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.settings.hint') }}</p>
      </div>
      <RouterLink to="/admin/share-scheduling" class="btn btn-secondary">{{ t('admin.shareScheduling.settings.openPage') }}</RouterLink>
    </div>
    <div v-if="loading && !loaded" class="mt-5 h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" role="status" :aria-label="t('common.loading')" />
    <div v-if="loadFailed" class="mt-4 flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.shareScheduling.settings.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="load">{{ t('admin.shareScheduling.settings.reload') }}</button>
    </div>
    <div v-if="loaded" class="mt-5 space-y-5">
      <div class="flex items-start justify-between gap-4">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.settings.enabled') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.settings.enabledHint') }}</p>
        </div>
        <Toggle v-model="draft.enabled" :disabled="saving" :aria-label="t('admin.shareScheduling.settings.enabled')" data-testid="share-enabled" />
      </div>
      <div class="flex items-start justify-between gap-4 border-t border-gray-100 pt-5 dark:border-dark-700">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.settings.cacheAttribution') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.settings.cacheAttributionHint') }}</p>
        </div>
        <Toggle v-model="draft.cache_attribution" :disabled="saving" :aria-label="t('admin.shareScheduling.settings.cacheAttribution')" data-testid="share-attribution" />
      </div>
      <div class="max-w-sm">
        <label for="share-sample-rate" class="input-label">{{ t('admin.shareScheduling.settings.sampleRate') }}</label>
        <input id="share-sample-rate" v-model.number="draft.attribution_sample_rate" type="number" min="1" :max="maxSampleRate" step="1" class="input" :disabled="saving" data-testid="share-sample-rate">
        <p class="input-hint">{{ t('admin.shareScheduling.settings.sampleRateHint') }}</p>
      </div>
      <div class="flex items-start justify-between gap-4 border-t border-gray-100 pt-5 dark:border-dark-700">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.settings.decisionSamples') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.settings.decisionSamplesHint') }}</p>
        </div>
        <Toggle v-model="draft.decision_samples" :disabled="saving" :aria-label="t('admin.shareScheduling.settings.decisionSamples')" data-testid="share-decision-samples" />
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" :disabled="saving || loading || !dirty" data-testid="share-settings-save" @click="save">{{ saving ? t('common.saving') : t('admin.shareScheduling.settings.save') }}</button>
        <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
        <p v-else-if="saved && !dirty" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ t('admin.shareScheduling.settings.saved') }}</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { adminAPI } from '@/api/admin'
import type { ShareSchedulingSettings } from '@/api/admin/shareScheduling'
import Toggle from '@/components/common/Toggle.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

const maxSampleRate = 64
const { t } = useI18n()
const draft = ref<ShareSchedulingSettings>({ enabled: false, cache_attribution: true, attribution_sample_rate: 4, decision_samples: true })
const baseline = ref('')
const loading = ref(false), saving = ref(false), loaded = ref(false), loadFailed = ref(false), saved = ref(false)
const error = ref('')
let disposed = false
const dirty = computed(() => JSON.stringify(draft.value) !== baseline.value)

function isSettings(value: unknown): value is ShareSchedulingSettings {
  if (!value || typeof value !== 'object') return false
  const s = value as Partial<ShareSchedulingSettings>
  return typeof s.enabled === 'boolean' && typeof s.cache_attribution === 'boolean' &&
    typeof s.decision_samples === 'boolean' && Number.isInteger(s.attribution_sample_rate)
}

function accept(value: ShareSchedulingSettings) {
  draft.value = { enabled: value.enabled, cache_attribution: value.cache_attribution, attribution_sample_rate: value.attribution_sample_rate, decision_samples: value.decision_samples }
  baseline.value = JSON.stringify(draft.value)
}

async function load() {
  if (loading.value || saving.value) return
  loading.value = true; loadFailed.value = false
  try {
    const settings = await adminAPI.shareScheduling.getSettings()
    if (disposed) return
    if (!isSettings(settings)) throw new Error('unexpected share scheduling settings')
    accept(settings)
    loaded.value = true
  } catch {
    if (!disposed) loadFailed.value = true
  } finally {
    if (!disposed) loading.value = false
  }
}

async function save() {
  if (saving.value || loading.value || !dirty.value) return
  error.value = ''; saved.value = false
  const rate = draft.value.attribution_sample_rate
  if (!Number.isInteger(rate) || rate < 1 || rate > maxSampleRate) {
    error.value = t('admin.shareScheduling.settings.sampleRateInvalid', { max: maxSampleRate })
    return
  }
  saving.value = true
  try {
    const result = await adminAPI.shareScheduling.updateSettings({ ...draft.value })
    if (disposed) return
    if (!isSettings(result)) throw new Error('unexpected share scheduling settings')
    accept(result)
    saved.value = true
  } catch (cause) {
    if (!disposed) error.value = extractApiErrorMessage(cause, t('admin.shareScheduling.settings.saveFailed'))
  } finally {
    if (!disposed) saving.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>
