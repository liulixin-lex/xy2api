<template>
  <fieldset class="space-y-4 border-t border-gray-200 pt-5 dark:border-dark-600">
    <div class="flex items-center justify-between gap-3">
      <label :for="uid + '-enabled'" class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.accounts.iqCheck') }}</label>
      <Toggle :id="uid + '-enabled'" class="focus:!ring-blue-500" :class="modelValue.enabled && '!bg-blue-600'" :model-value="modelValue.enabled" :disabled="!included('enabled')" :aria-label="t('admin.accounts.iqCheck')" @update:model-value="setValue('enabled', $event)" />
    </div>
    <div class="grid grid-cols-1 items-start gap-4 sm:grid-cols-3">
      <div class="min-w-0">
        <label :for="uid + '-interval'" class="input-label">{{ t('admin.accounts.iqInterval') }}</label>
        <input :id="uid + '-interval'" type="number" class="input w-full" min="1" max="1440" step="1" required :disabled="!included('interval_minutes')" :value="modelValue.interval_minutes" @input="updateNumber('interval_minutes', $event)" />
      </div>
      <div class="min-w-0">
        <label :for="uid + '-model'" class="input-label">{{ t('admin.accounts.iqModel') }}</label>
        <Select :id="uid + '-model'" :model-value="modelValue.model ?? 'gpt-6-astra'" :options="modelOptions" searchable creatable :creatable-prefix="t('admin.accounts.iqUseCustom')" :search-placeholder="t('admin.accounts.iqModelSearch')" :aria-label="t('admin.accounts.iqModel')" :disabled="!included('model')" :loading="loading" :error="!!modelError" @update:model-value="setValue('model', $event)" />
        <p v-if="modelError" role="alert" class="mt-1 text-sm text-red-600 dark:text-red-400">{{ modelError }}</p>
      </div>
      <div class="min-w-0">
        <label :for="uid + '-effort'" class="input-label">{{ t('admin.accounts.iqEffort') }}</label>
        <Select :id="uid + '-effort'" :model-value="modelValue.reasoning_effort ?? 'low'" :options="effortOptions" searchable :creatable="!authoritativeEffort" :creatable-prefix="t('admin.accounts.iqUseCustom')" :aria-label="t('admin.accounts.iqEffort')" :disabled="!included('reasoning_effort')" :error="!!effortError" @update:model-value="setValue('reasoning_effort', $event)" />
        <p v-if="effortError" role="alert" class="mt-1 text-sm text-red-600 dark:text-red-400">{{ effortError }}</p>
      </div>
    </div>
    <div v-if="accountId" class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <button type="button" data-testid="iq-sync-models" class="inline-flex items-center gap-1.5 rounded-lg border border-emerald-200 px-3 py-1.5 text-sm text-emerald-700 transition-colors hover:bg-emerald-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-emerald-800 dark:text-emerald-400 dark:hover:bg-emerald-900/30" :disabled="loading || discoveryDisabled || !included('model')" @click="fetchModels">
        <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
        {{ t(loading ? 'admin.accounts.syncUpstreamModelsLoading' : 'admin.accounts.syncUpstreamModels') }}
      </button>
      <p class="min-w-0 text-xs" :class="failed ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'" aria-live="polite">{{ discoveryDisabled ? t('admin.accounts.iqSaveCredentials') : catalogMessage }}</p>
    </div>
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div class="min-w-0">
        <label :for="uid + '-schedule'" class="input-label">{{ t('admin.accounts.iqSchedule') }}</label>
        <Select :id="uid + '-schedule'" :model-value="modelValue.scheduling_mode ?? 'fixed'" :options="scheduleOptions" :aria-label="t('admin.accounts.iqSchedule')" :disabled="!included('scheduling_mode')" @update:model-value="setValue('scheduling_mode', $event)" />
      </div>
      <div v-if="modelValue.scheduling_mode === 'adaptive' || fields?.includes('max_interval_minutes')" class="min-w-0">
        <label :for="uid + '-max-interval'" class="input-label">{{ t('admin.accounts.iqMaxInterval') }}</label>
        <input :id="uid + '-max-interval'" type="number" class="input w-full" min="1" max="1440" :value="modelValue.max_interval_minutes ?? 60" :disabled="!included('max_interval_minutes')" required @input="updateNumber('max_interval_minutes', $event)" />
      </div>
    </div>
    <details class="border-t border-gray-100 pt-3 dark:border-dark-700" :open="!!fields">
      <summary class="cursor-pointer text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.accounts.iqAdvanced') }}</summary>
      <div class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div v-for="field in monitoringNumbers" :key="field.key" class="min-w-0">
          <label :for="uid + '-' + field.key" class="input-label">{{ t(field.label) }}</label>
          <input :id="uid + '-' + field.key" type="number" class="input w-full" :min="field.min" :max="field.max" :value="modelValue[field.key] ?? field.fallback" :disabled="!included(field.key)" required @input="updateNumber(field.key, $event)" />
        </div>
        <div class="min-w-0">
          <label :for="uid + '-output'" class="input-label">{{ t('admin.accounts.iqOutputMode') }}</label>
          <Select :id="uid + '-output'" :model-value="modelValue.output_mode ?? 'compat'" :options="outputOptions" :aria-label="t('admin.accounts.iqOutputMode')" :disabled="!included('output_mode')" @update:model-value="setValue('output_mode', $event)" />
        </div>
        <div class="min-w-0">
          <label :for="uid + '-group'" class="input-label">{{ t('admin.accounts.iqQuotaGroup') }}</label>
          <input :id="uid + '-group'" class="input w-full" maxlength="64" pattern="[a-zA-Z0-9_-]*" :value="modelValue.quota_group ?? ''" :disabled="!included('quota_group')" :placeholder="t('admin.accounts.iqGroupPlaceholder')" @input="setValue('quota_group', ($event.target as HTMLInputElement).value)" />
        </div>
      </div>
    </details>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { getIQCheckModels } from '@/api/admin/accounts'
import type { IQCheckSettings, IQModelCatalog } from '@/types'

const props = defineProps<{ modelValue: IQCheckSettings; accountId?: number; discoveryDisabled?: boolean; fields?: (keyof IQCheckSettings)[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: IQCheckSettings]; validity: [valid: boolean] }>()
const { t, te } = useI18n()
const uid = 'iq-' + getCurrentInstance()?.uid
const included = (field: keyof IQCheckSettings) => !props.fields || props.fields.includes(field)
const catalog = ref<IQModelCatalog | null>(null)
const loading = ref(false)
const failed = ref(false)
let request = 0
let controller: AbortController | undefined
const selectedModel = computed(() => catalog.value?.models.find(item => item.id === (props.modelValue.model ?? 'gpt-6-astra')))
const modelOptions = computed(() => {
  const ids = new Set(catalog.value?.models.map(item => item.id) ?? [])
  ids.add(props.modelValue.model ?? 'gpt-6-astra')
  return [...ids].filter(Boolean).map(value => ({ value, label: value }))
})
const authoritativeEffort = computed(() => selectedModel.value?.reasoning === false || selectedModel.value?.capability_sources.supported_reasoning_levels === 'upstream')
const effortOptions = computed(() => {
  const model = selectedModel.value
  const levels = model?.reasoning === false ? ['none'] : model?.supported_reasoning_levels?.length ? model.supported_reasoning_levels : ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
  const current = props.modelValue.reasoning_effort ?? 'low'
  return [...new Set(['upstream_default', ...levels, current])].map(value => ({
    value, label: te('admin.accounts.iqEfforts.' + value) ? t('admin.accounts.iqEfforts.' + value) : value,
    disabled: authoritativeEffort.value && value !== 'upstream_default' && !levels.includes(value)
  }))
})
const modelError = computed(() => {
  const model = props.modelValue.model ?? 'gpt-6-astra'
  return included('model') && (!model.trim() || model.length > 256 || /\s/.test(model)) ? t('admin.accounts.iqModelInvalid') : ''
})
const effortError = computed(() => {
  if (!included('reasoning_effort')) return ''
  const effort = props.modelValue.reasoning_effort ?? 'low'
  if (!/^[a-z0-9_-]{1,32}$/.test(effort)) return t('admin.accounts.iqEffortInvalid')
  return effortOptions.value.find(option => option.value === effort)?.disabled ? t('admin.accounts.iqEffortUnsupported') : ''
})
watch([modelError, effortError], ([model, effort]) => emit('validity', !model && !effort), { immediate: true })
const catalogMessage = computed(() => failed.value || catalog.value?.error ? t('admin.accounts.iqModelsFailed') : catalog.value?.stale ? t('admin.accounts.iqModelsStale') : catalog.value ? catalog.value.models.length ? t('admin.accounts.iqModelsReady', { count: catalog.value.models.length }) : t('admin.accounts.iqModelsEmpty') : '')
const scheduleOptions = computed(() => [{ value: 'fixed', label: t('admin.accounts.iqFixed') }, { value: 'adaptive', label: t('admin.accounts.iqAdaptive') }])
const outputOptions = computed(() => [{ value: 'compat', label: t('admin.accounts.iqCompat') }, { value: 'strict', label: t('admin.accounts.iqStrict') }])
function clearCatalog() { request++; controller?.abort(); catalog.value = null; failed.value = false; loading.value = false }
watch([() => props.accountId, () => props.discoveryDisabled], clearCatalog)
onBeforeUnmount(clearCatalog)
async function fetchModels() {
  if (!props.accountId || props.discoveryDisabled || loading.value || !included('model')) return
  const version = ++request
  controller = new AbortController()
  loading.value = true
  failed.value = false
  try { const result = await getIQCheckModels(props.accountId, true, controller.signal); if (version === request) catalog.value = result }
  catch { if (version === request) failed.value = true }
  finally { if (version === request) loading.value = false }
}
function setValue(field: keyof IQCheckSettings, value: string | number | boolean | null) {
  if (!included(field)) return
  emit('update:modelValue', { ...props.modelValue, [field]: typeof value === 'string' ? value.trim() : value })
}
type NumericSetting = 'interval_minutes' | 'max_interval_minutes' | 'daily_request_limit' | 'timeout_seconds'
const monitoringNumbers = computed(() => [
  { key: 'daily_request_limit' as const, label: 'admin.accounts.iqDailyLimit', min: 1, max: 1440, fallback: Math.ceil(1440 / Math.max(1, props.modelValue.interval_minutes)) },
  { key: 'timeout_seconds' as const, label: 'admin.accounts.iqTimeout', min: 30, max: 300, fallback: 120 }
])
const updateNumber = (field: NumericSetting, event: Event) => setValue(field, Number((event.target as HTMLInputElement).value))
</script>
