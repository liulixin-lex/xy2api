<template>
  <fieldset class="space-y-4 border-t border-gray-200 pt-5 dark:border-dark-600" data-testid="iq-settings">
    <div>
      <p class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.accounts.iqCheck') }}</p>
      <p class="input-hint">{{ t(bulk ? 'admin.accounts.iqBulkHint' : 'admin.accounts.iqTargetsHint') }}</p>
    </div>

    <div v-if="loading" role="status" class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}</div>
    <div v-else-if="loadFailed" role="alert" class="flex flex-wrap items-center gap-3 text-sm text-red-600 dark:text-red-400">
      {{ t('admin.accounts.iqStatusFailed') }}
      <button type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('common.refresh') }}</button>
    </div>

    <template v-else>
      <div class="space-y-3">
        <div class="hidden gap-3 text-xs font-medium text-gray-500 dark:text-dark-400 sm:grid" :class="gridClass" aria-hidden="true">
          <span>{{ t('admin.accounts.iqModel') }}</span><span>{{ t('admin.accounts.iqEnabled') }}</span><span>{{ t('admin.accounts.iqEffort') }}</span><span>{{ t('admin.accounts.iqInterval') }}</span><span />
        </div>
        <div v-for="row in rows" :key="row.key" class="space-y-1" data-testid="iq-target-row">
          <div class="grid grid-cols-2 items-center gap-3" :class="gridClass">
            <div class="col-span-2 min-w-0 sm:col-span-1">
              <Select v-if="row.isNew" :id="`${uid}-${row.key}-model`" :model-value="row.model" :options="modelOptions(row.model)" searchable creatable :creatable-prefix="t('admin.accounts.iqUseCustom')" :search-placeholder="t('admin.accounts.iqModelSearch')" :placeholder="t('admin.accounts.iqModelSearch')" :aria-label="t('admin.accounts.iqModel')" :loading="catalogLoading" :error="!!rowError(row)" @update:model-value="row.model = String($event ?? '').trim()" />
              <span v-else class="block truncate text-sm font-medium text-gray-900 dark:text-gray-100" :title="row.model">{{ row.model }}</span>
            </div>
            <div>
              <Select v-if="bulk" :model-value="row.enabled === null ? '' : row.enabled ? 'on' : 'off'" :options="bulkEnabledOptions" :aria-label="`${row.model || t('admin.accounts.iqModel')} ${t('admin.accounts.iqEnabled')}`" @update:model-value="row.enabled = $event === '' ? null : $event === 'on'" />
              <Toggle v-else :model-value="!!row.enabled" :aria-label="`${row.model} ${t('admin.accounts.iqEnabled')}`" @update:model-value="row.enabled = $event" />
            </div>
            <div class="min-w-0">
              <Select :id="`${uid}-${row.key}-effort`" :model-value="row.reasoning_effort" :options="effortOptions(row)" searchable :creatable="!authoritative(row.model)" :creatable-prefix="t('admin.accounts.iqUseCustom')" :aria-label="`${row.model || t('admin.accounts.iqModel')} ${t('admin.accounts.iqEffort')}`" :aria-describedby="`${uid}-${row.key}-effort-source`" :error="!!effortError(row)" @update:model-value="row.reasoning_effort = String($event ?? '').trim()" />
            </div>
            <input type="number" class="input w-full" min="1" max="1440" step="1" :placeholder="bulk ? t('admin.accounts.iqKeep') : ''" :aria-label="`${row.model || t('admin.accounts.iqModel')} ${t('admin.accounts.iqInterval')}`" :value="row.interval_minutes ?? ''" @input="row.interval_minutes = numberValue($event)" />
            <button v-if="row.isNew || !isIQBuiltinModel(row.model)" type="button" class="btn btn-ghost h-9 w-9 p-0 text-gray-500 hover:text-red-600" :aria-label="t('admin.accounts.iqRemoveTarget', { model: row.model })" :title="t('admin.accounts.iqRemoveTarget', { model: row.model })" @click="removeRow(row)">
              <Icon name="trash" size="sm" />
            </button>
            <span v-else />
          </div>
          <p :id="`${uid}-${row.key}-effort-source`" class="text-xs text-gray-500 dark:text-dark-400">{{ capabilitySource(row.model) }}<template v-if="row.reasoning_effort === 'upstream_default'"> {{ t('admin.accounts.iqDefaultEffortHint') }}</template></p>
          <p v-if="rowError(row)" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ rowError(row) }}</p>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <button type="button" class="btn btn-secondary btn-sm inline-flex items-center gap-1.5" data-testid="iq-add-target" @click="addRow"><Icon name="plus" size="sm" />{{ t('admin.accounts.iqAddTarget') }}</button>
        <template v-if="accountId">
          <button type="button" data-testid="iq-sync-models" class="btn btn-secondary btn-sm inline-flex items-center gap-1.5" :disabled="catalogLoading || discoveryDisabled" @click="fetchModels">
            <Icon name="refresh" size="sm" :class="catalogLoading && 'animate-spin'" />{{ t(catalogLoading ? 'admin.accounts.syncUpstreamModelsLoading' : 'admin.accounts.syncUpstreamModels') }}
          </button>
          <div class="min-w-0 space-y-1 text-xs text-gray-500 dark:text-dark-400" aria-live="polite">
            <p v-if="discoveryDisabled">{{ t('admin.accounts.iqSaveCredentials') }}</p>
            <template v-else>
              <p v-if="catalog">{{ catalog.models.length ? t('admin.accounts.iqModelsReady', { count: catalog.models.length }) : t('admin.accounts.iqModelsEmpty') }}</p>
              <p v-if="catalog?.from_cache">{{ t('admin.accounts.iqModelsCached') }}</p>
              <p v-if="catalog?.stale" class="text-amber-600 dark:text-amber-400">{{ t('admin.accounts.iqModelsStale') }}</p>
              <p v-if="catalogFailed || catalog?.error" class="text-red-600 dark:text-red-400">{{ t('admin.accounts.iqModelsFailed') }}</p>
            </template>
          </div>
        </template>
      </div>

      <details class="border-t border-gray-100 pt-3 dark:border-dark-700">
        <summary class="cursor-pointer text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.accounts.iqAdvanced') }}</summary>
        <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label v-for="row in rows" :key="row.key" class="min-w-0">
            <span class="input-label truncate">{{ t('admin.accounts.iqTimeoutFor', { model: row.model || '—' }) }}</span>
            <input type="number" class="input w-full" min="30" max="300" step="1" :placeholder="bulk ? t('admin.accounts.iqKeep') : ''" :value="row.timeout_seconds ?? ''" data-testid="iq-timeout" @input="row.timeout_seconds = numberValue($event)" />
          </label>
        </div>
      </details>
    </template>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { getIQCheck, getIQCheckModels } from '@/api/admin/accounts'
import type { IQModelCatalog, IQTargetSettings, IQTargetsPatch } from '@/types'
import { IQ_BUILTIN_MODELS, isIQBuiltinModel, sortIQTargets } from '@/components/admin/account/iqCheckPresentation'

// Edit mode (accountId) loads the account's targets and emits only changed fields; bulk mode starts from the built-in
// targets with every field on "keep" and emits only the fields the admin set.
const props = defineProps<{ accountId?: number; bulk?: boolean; discoveryDisabled?: boolean }>()
const emit = defineEmits<{ patch: [patch: IQTargetsPatch | null]; validity: [valid: boolean] }>()
const { t } = useI18n()
const uid = 'iq-' + getCurrentInstance()?.uid
const gridClass = 'sm:grid-cols-[minmax(0,1.5fr)_auto_minmax(0,1fr)_5.5rem_2.25rem]'

type Field = 'enabled' | 'reasoning_effort' | 'interval_minutes' | 'timeout_seconds'
type Row = { key: number; model: string; enabled: boolean | null; reasoning_effort: string; interval_minutes: number | null; timeout_seconds: number | null; isNew: boolean }
const fields: Field[] = ['enabled', 'reasoning_effort', 'interval_minutes', 'timeout_seconds']
let nextKey = 0
const rows = ref<Row[]>([])
const original = new Map<string, Row>()
const loading = ref(false)
const loadFailed = ref(false)
const numberValue = (event: Event) => { const value = (event.target as HTMLInputElement).value; return value === '' ? null : Number(value) }

function blankRow(model: string, isNew: boolean): Row {
  return props.bulk
    ? { key: nextKey++, model, enabled: null, reasoning_effort: '', interval_minutes: null, timeout_seconds: null, isNew }
    : { key: nextKey++, model, enabled: true, reasoning_effort: 'low', interval_minutes: 5, timeout_seconds: 120, isNew }
}
const addRow = () => { rows.value.push(blankRow('', true)) }
const removeRow = (row: Row) => { rows.value = rows.value.filter(item => item !== row) }

let loadVersion = 0
async function load() {
  original.clear()
  if (props.bulk || !props.accountId) { rows.value = IQ_BUILTIN_MODELS.map(model => blankRow(model, false)); return }
  const version = ++loadVersion
  loading.value = true; loadFailed.value = false; rows.value = []
  try {
    const targets = sortIQTargets(await getIQCheck(props.accountId))
    if (version !== loadVersion) return
    const loaded = targets.map(target => ({ key: nextKey++, model: target.model, enabled: target.enabled, reasoning_effort: target.reasoning_effort, interval_minutes: target.interval_minutes, timeout_seconds: target.timeout_seconds, isNew: false }))
    for (const row of loaded) original.set(row.model, { ...row })
    rows.value = loaded
  } catch { if (version === loadVersion) loadFailed.value = true }
  finally { if (version === loadVersion) loading.value = false }
}
watch(() => props.accountId, load, { immediate: true })

// ---- model catalog and effort capabilities ----
const catalog = ref<IQModelCatalog | null>(null)
const catalogLoading = ref(false)
const catalogFailed = ref(false)
let catalogRequest = 0
let controller: AbortController | undefined
function clearCatalog() { catalogRequest++; controller?.abort(); catalog.value = null; catalogFailed.value = false; catalogLoading.value = false }
watch([() => props.accountId, () => props.discoveryDisabled], clearCatalog)
onBeforeUnmount(() => { clearCatalog(); loadVersion++ })
async function fetchModels() {
  if (!props.accountId || props.discoveryDisabled || catalogLoading.value) return
  const version = ++catalogRequest
  controller = new AbortController()
  catalogLoading.value = true; catalogFailed.value = false
  try { const result = await getIQCheckModels(props.accountId, true, controller.signal); if (version === catalogRequest) catalog.value = result }
  catch { if (version === catalogRequest) catalogFailed.value = true }
  finally { if (version === catalogRequest) catalogLoading.value = false }
}
const modelOptions = (current: string) => [...new Set([...(catalog.value?.models.map(item => item.id) ?? []), current].filter(Boolean))].map(value => ({ value, label: value }))
const catalogModel = (model: string) => catalog.value?.models.find(item => item.id === model)
// Reference-only hints from the pinned GPT-6.1 Sol descriptor. Upstream capabilities stay authoritative.
const gpt61SolReferenceLevels = ['low', 'medium', 'high', 'xhigh', 'max', 'ultra']
const isGPT61Sol = (model: string) => /(?:^|\/)gpt-6\.1-sol(?:-(?:none|minimal|low|medium|high|xhigh|max|ultra))?(?:\/compact)?$/.test(model.trim().toLowerCase().replace(/_/g, '-'))
const authoritative = (model: string) => catalogModel(model)?.reasoning === false || catalogModel(model)?.capability_sources.supported_reasoning_levels === 'upstream'
function useSolReference(model: string) {
  const item = catalogModel(model)
  return isGPT61Sol(model) && item?.reasoning !== false && item?.capability_sources.supported_reasoning_levels !== 'upstream' && !item?.supported_reasoning_levels?.length
}
function levelsFor(model: string) {
  const item = catalogModel(model)
  if (item?.reasoning === false) return ['none']
  if (isGPT61Sol(model) && authoritative(model)) return item?.supported_reasoning_levels ?? []
  if (useSolReference(model)) return gpt61SolReferenceLevels
  if (item?.supported_reasoning_levels?.length) return item.supported_reasoning_levels
  return model === 'gpt-6-astra' ? ['low', 'medium', 'high', 'xhigh', 'max'] : ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
}
function effortOptions(row: Row) {
  const levels = levelsFor(row.model)
  const values = [...new Set(['upstream_default', ...levels, row.reasoning_effort].filter(Boolean))]
  const options = values.map(value => ({
    value, label: value === 'upstream_default' ? 'default' : value,
    disabled: (authoritative(row.model) && value !== 'upstream_default' && !levels.includes(value)) || (useSolReference(row.model) && ['none', 'minimal'].includes(value))
  }))
  return props.bulk ? [{ value: '', label: t('admin.accounts.iqKeep'), disabled: false }, ...options] : options
}
function capabilitySource(model: string) {
  const item = catalogModel(model)
  const source = item?.capability_sources[item.reasoning === false ? 'reasoning' : 'supported_reasoning_levels']
  return source === 'upstream' ? t('admin.accounts.iqCapabilitiesUpstream') : source === 'reference' || useSolReference(model) ? t('admin.accounts.iqCapabilitiesReference') : t('admin.accounts.iqCapabilitiesUnknown')
}
const bulkEnabledOptions = computed(() => [{ value: '', label: t('admin.accounts.iqKeep') }, { value: 'on', label: t('admin.accounts.iqEnable') }, { value: 'off', label: t('admin.accounts.iqDisable') }])

// ---- validation ----
function effortError(row: Row) {
  if (props.bulk && row.reasoning_effort === '') return ''
  if (!/^[a-z0-9_-]{1,32}$/.test(row.reasoning_effort)) return t('admin.accounts.iqEffortInvalid')
  return effortOptions(row).find(option => option.value === row.reasoning_effort)?.disabled ? t('admin.accounts.iqEffortUnsupported') : ''
}
const outOfRange = (value: number | null, min: number, max: number) => value !== null && (!Number.isInteger(value) || value < min || value > max)
function rowError(row: Row) {
  const model = row.model
  if (!model || model.length > 256 || /\s/.test(model)) return t('admin.accounts.iqModelInvalid')
  if (rows.value.filter(item => item.model === model).length > 1) return t('admin.accounts.iqModelDuplicate')
  if (outOfRange(row.interval_minutes, 1, 1440) || (!props.bulk && row.interval_minutes === null)) return t('admin.accounts.iqIntervalInvalid')
  if (outOfRange(row.timeout_seconds, 30, 300) || (!props.bulk && row.timeout_seconds === null)) return t('admin.accounts.iqTimeoutInvalid')
  return effortError(row)
}
// While loading or after a failed load there are no rows, so the account form stays savable without IQ changes.
const valid = computed(() => rows.value.every(row => !rowError(row)))

// ---- patch ----
const patch = computed<IQTargetsPatch | null>(() => {
  if (!valid.value) return null
  const targets: IQTargetSettings[] = []
  for (const row of rows.value) {
    const before = original.get(row.model)
    const changed = fields.filter(field => (props.bulk ? row[field] !== null && row[field] !== '' : !before || row[field] !== before[field]))
    if (!changed.length && !(props.bulk && row.isNew)) continue
    targets.push(Object.fromEntries([['model', row.model], ...changed.map(field => [field, row[field]])]) as unknown as IQTargetSettings)
  }
  const remove = [...original.keys()].filter(model => !rows.value.some(row => row.model === model))
  if (!targets.length && !remove.length) return null
  return { ...(targets.length ? { targets } : {}), ...(remove.length ? { remove } : {}) }
})
watch(valid, value => emit('validity', value), { immediate: true })
watch(patch, value => emit('patch', value), { immediate: true, deep: true })
</script>
