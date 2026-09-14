<template>
  <fieldset class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="flex items-center gap-2 text-sm font-medium">
      <input type="checkbox" class="rounded text-blue-500 focus:ring-blue-500" :disabled="!included('enabled')" :checked="modelValue.enabled" @change="updateEnabled" />
      {{ t('admin.accounts.iqCheck') }}
    </label>
    <div class="grid grid-cols-1 items-start gap-4 sm:grid-cols-3">
      <label class="block min-w-0 text-sm">
        {{ t('admin.accounts.iqInterval') }}
        <input type="number" class="input mt-1 w-full" min="1" max="1440" step="1" required :disabled="!included('interval_minutes')" :value="modelValue.interval_minutes" @input="updateInterval" />
      </label>
      <div class="min-w-0">
        <label :for="`${uid}-model`" class="block text-sm">{{ t('admin.accounts.iqModel') }}</label>
        <input :id="`${uid}-model`" class="input mt-1 w-full" :list="`${uid}-models`" :value="modelValue.model ?? 'gpt-6-astra'" :disabled="!included('model')" required maxlength="256" autocomplete="off" :aria-describedby="`${uid}-catalog`" @input="updateText('model', $event)" />
        <datalist :id="`${uid}-models`"><option v-for="item in catalog?.models" :key="item.id" :value="item.id">{{ item.display_name }}</option></datalist>
        <button v-if="accountId" type="button" class="mt-2 text-sm text-blue-700 underline underline-offset-2 hover:text-blue-800 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400" :disabled="loading || discoveryDisabled" @click="fetchModels">{{ t(loading ? 'common.loading' : 'admin.accounts.iqPullModels') }}</button>
        <p :id="`${uid}-catalog`" class="input-hint break-words" aria-live="polite">{{ discoveryDisabled ? t('admin.accounts.iqSaveCredentials') : catalogMessage }}</p>
        <p v-if="catalog?.fetched_at" class="input-hint">{{ formatDateTime(catalog.fetched_at) }}</p>
      </div>
      <div class="min-w-0">
        <label :for="`${uid}-effort`" class="block text-sm">{{ t('admin.accounts.iqEffort') }}</label>
        <input :id="`${uid}-effort`" ref="effortInput" class="input mt-1 w-full" :list="`${uid}-efforts`" :value="modelValue.reasoning_effort ?? 'low'" :disabled="!included('reasoning_effort')" required maxlength="32" pattern="[a-z0-9_-]+" autocomplete="off" :aria-invalid="!!effortError" :aria-describedby="`${uid}-effort-help`" @input="updateText('reasoning_effort', $event)" />
        <datalist :id="`${uid}-efforts`"><option value="upstream_default">{{ t('admin.accounts.iqUpstreamDefault') }}</option><option v-for="level in effortOptions" :key="level" :value="level" /></datalist>
        <p :id="`${uid}-effort-help`" class="input-hint">{{ t('admin.accounts.iqEffortHint') }} {{ capabilitySource }}</p>
        <p v-if="effortError" role="alert" class="mt-1 text-sm text-red-600 dark:text-red-400">{{ effortError }}</p>
      </div>
    </div>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <label class="min-w-0"><span class="input-label">{{ t('admin.accounts.iqSchedule') }}</span>
        <select class="input" :value="modelValue.scheduling_mode ?? 'fixed'" :disabled="!included('scheduling_mode')" @change="updateText('scheduling_mode', $event)"><option value="fixed">{{ t('admin.accounts.iqFixed') }}</option><option value="adaptive">{{ t('admin.accounts.iqAdaptive') }}</option></select>
      </label>
      <label v-for="field in monitoringNumbers" :key="field.key" class="min-w-0"><span class="input-label">{{ t(field.label) }}</span><input type="number" class="input" :min="field.min" :max="field.max" :value="modelValue[field.key] ?? field.fallback" :disabled="!included(field.key)" required @input="updateNumber(field.key, $event)" /></label>
      <label class="min-w-0"><span class="input-label">{{ t('admin.accounts.iqQuotaGroup') }}</span><input class="input" maxlength="64" pattern="[a-zA-Z0-9_-]*" :value="modelValue.quota_group ?? ''" :disabled="!included('quota_group')" @input="updateText('quota_group', $event)" /><span class="input-hint">{{ t('admin.accounts.iqQuotaGroupHint') }}</span></label>
    </div>
    <p class="input-hint">{{ t('admin.accounts.iqScheduleHint', { count: Math.ceil(1440 / Math.max(1, modelValue.interval_minutes)), interval: modelValue.scheduling_mode === 'adaptive' ? Math.max(modelValue.interval_minutes, modelValue.max_interval_minutes ?? 60) : modelValue.interval_minutes }) }}</p>
    <details>
      <summary class="cursor-pointer text-sm">{{ t('admin.accounts.iqOutputSettings') }}</summary>
      <label class="mt-2 block text-sm">
        {{ t('admin.accounts.iqOutputMode') }}
        <select class="input mt-1 w-full" :value="modelValue.output_mode ?? 'compat'" :disabled="!included('output_mode')" @change="updateText('output_mode', $event)">
          <option value="compat">{{ t('admin.accounts.iqCompat') }}</option>
          <option value="strict">{{ t('admin.accounts.iqStrict') }}</option>
        </select>
      </label>
      <p class="input-hint">{{ t('admin.accounts.iqStrictHint') }}</p>
    </details>
    <p class="input-hint">{{ t('admin.accounts.iqHint') }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getIQCheckModels } from '@/api/admin/accounts'
import { formatDateTime } from '@/utils/format'
import type { IQCheckSettings, IQModelCatalog } from '@/types'
const props = defineProps<{ modelValue: IQCheckSettings; accountId?: number; discoveryDisabled?: boolean; fields?: (keyof IQCheckSettings)[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: IQCheckSettings]; validity: [valid: boolean] }>()
const { t } = useI18n()
const uid = `iq-${getCurrentInstance()?.uid}`
const included = (field: keyof IQCheckSettings) => !props.fields || props.fields.includes(field)
const catalog = ref<IQModelCatalog | null>(null)
const loading = ref(false)
const failed = ref(false)
const effortInput = ref<HTMLInputElement>()
let request = 0
let controller: AbortController | undefined
const selectedModel = computed(() => catalog.value?.models.find(item => item.id === props.modelValue.model))
const effortOptions = computed(() => selectedModel.value?.supported_reasoning_levels?.length ? selectedModel.value.supported_reasoning_levels : props.modelValue.model === 'gpt-6-astra' || !props.modelValue.model ? ['low', 'medium', 'high', 'xhigh', 'max'] : ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
const capabilitySource = computed(() => {
  const source = selectedModel.value?.capability_sources.supported_reasoning_levels
  return t(source === 'upstream' ? 'admin.accounts.iqCapabilitiesUpstream' : source === 'reference' || props.modelValue.model === 'gpt-6-astra' || !props.modelValue.model ? 'admin.accounts.iqCapabilitiesReference' : 'admin.accounts.iqCapabilitiesUnknown')
})
const effortError = computed(() => {
  if (!included('reasoning_effort')) return ''
  const effort = props.modelValue.reasoning_effort ?? 'low'
  if (effort === 'upstream_default') return ''
  const model = selectedModel.value
  const authoritative = model?.capability_sources.supported_reasoning_levels === 'upstream'
  if (model?.reasoning === false && effort !== 'none' || authoritative && model?.supported_reasoning_levels?.length && !model.supported_reasoning_levels.includes(effort)) return t('admin.accounts.iqEffortUnsupported')
  return ''
})
watch([effortError, effortInput], ([error, input]) => { input?.setCustomValidity(error); emit('validity', !error) }, { immediate: true })
const catalogMessage = computed(() => t(failed.value || catalog.value?.error ? 'admin.accounts.iqModelsFailed' : catalog.value?.stale ? 'admin.accounts.iqModelsStale' : catalog.value?.from_cache ? 'admin.accounts.iqModelsCached' : catalog.value ? catalog.value.models.length ? 'admin.accounts.iqModelsUpstream' : 'admin.accounts.iqModelsEmpty' : 'admin.accounts.iqModelHint'))
function clearCatalog() { request++; controller?.abort(); catalog.value = null; failed.value = false; loading.value = false }
watch([() => props.accountId, () => props.discoveryDisabled], clearCatalog)
onBeforeUnmount(clearCatalog)
async function fetchModels() {
  if (!props.accountId || props.discoveryDisabled || loading.value) return
  const version = ++request
  controller = new AbortController()
  loading.value = true; failed.value = false
  try { const result = await getIQCheckModels(props.accountId, !!catalog.value, controller.signal); if (version === request) catalog.value = result }
  catch { if (version === request) failed.value = true }
  finally { if (version === request) loading.value = false }
}
const updateEnabled = (event: Event) => emit('update:modelValue', { ...props.modelValue, enabled: (event.target as HTMLInputElement).checked })
const updateInterval = (event: Event) => emit('update:modelValue', { ...props.modelValue, interval_minutes: Number((event.target as HTMLInputElement).value) })
const updateText = (field: 'model' | 'reasoning_effort' | 'output_mode' | 'scheduling_mode' | 'quota_group', event: Event) => emit('update:modelValue', { ...props.modelValue, [field]: (event.target as HTMLInputElement).value })
type NumericSetting = 'max_interval_minutes' | 'daily_request_limit' | 'timeout_seconds'
const monitoringNumbers = computed(() => [
  { key: 'max_interval_minutes' as NumericSetting, label: 'admin.accounts.iqMaxInterval', min: 1, max: 1440, fallback: 60 },
  { key: 'daily_request_limit' as NumericSetting, label: 'admin.accounts.iqDailyLimit', min: 1, max: 1440, fallback: Math.ceil(1440 / Math.max(1, props.modelValue.interval_minutes)) },
  { key: 'timeout_seconds' as NumericSetting, label: 'admin.accounts.iqTimeout', min: 30, max: 300, fallback: 120 }
])
const updateNumber = (field: NumericSetting, event: Event) => emit('update:modelValue', { ...props.modelValue, [field]: Number((event.target as HTMLInputElement).value) })
</script>
