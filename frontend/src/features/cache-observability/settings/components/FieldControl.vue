<template>
  <div class="min-w-0" :data-test="`control-${field.field}`">
    <Toggle
      v-if="kind === 'bool'"
      :id="controlId"
      :model-value="value === true"
      :disabled="disabled"
      :aria-label="label"
      :aria-describedby="describedBy"
      :class="disabled ? 'cursor-not-allowed opacity-50' : ''"
      @update:model-value="emit('update', $event)"
    />

    <div v-else-if="kind === 'int'" class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <input
        :id="controlId"
        type="number"
        inputmode="numeric"
        class="input w-28 text-right tabular-nums"
        :class="invalid ? 'input-error' : ''"
        :min="field.min"
        :max="field.max"
        step="1"
        :value="typeof value === 'number' ? value : ''"
        :disabled="disabled"
        :aria-label="label"
        :aria-describedby="describedBy"
        :aria-invalid="invalid || undefined"
        @change="onNumber(($event.target as HTMLInputElement).value)"
      />
      <span v-if="field.unit" class="text-sm text-gray-600 dark:text-dark-300">{{ field.unit === 'percent' ? '%' : field.unit }}</span>
      <span v-if="rangeText" class="w-full text-xs text-gray-500 dark:text-dark-400">{{ rangeText }}</span>
    </div>

    <div v-else-if="kind === 'enum'" class="w-full sm:w-72">
      <Select
        :id="controlId"
        :model-value="String(value ?? '')"
        :options="enumOptions"
        :disabled="disabled"
        :error="invalid"
        :aria-label="label"
        :aria-describedby="describedBy"
        @update:model-value="emit('update', $event)"
      />
    </div>

    <fieldset v-else-if="kind === 'enum_list'" :aria-describedby="describedBy" class="flex flex-wrap gap-x-5 gap-y-2">
      <legend class="sr-only">{{ label }}</legend>
      <label v-for="option in options" :key="option" class="inline-flex items-center gap-2 text-sm text-gray-800 dark:text-gray-200">
        <input
          type="checkbox"
          class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-800"
          :checked="Array.isArray(value) && value.includes(option)"
          :disabled="disabled"
          @change="toggleOption(option, ($event.target as HTMLInputElement).checked)"
        />
        {{ codeLabel('options', option, t, te) }}
      </label>
    </fieldset>

    <div v-else-if="kind === 'id_list' && refKind === 'group'" class="w-full max-w-xl">
      <Select
        :id="controlId"
        :model-value="null"
        :options="groupOptions"
        :disabled="disabled || groupsFailed"
        :aria-label="label"
        :aria-describedby="describedBy"
        :placeholder="t('admin.cacheSettings.groupPicker.placeholder')"
        :empty-text="t('admin.cacheSettings.groupPicker.empty')"
        searchable
        @update:model-value="addId"
      />
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.groupPicker.servingScope') }}</p>
      <p v-if="groupsFailed" class="mt-1 text-xs text-amber-800 dark:text-amber-300">{{ t('admin.cacheSettings.groupPicker.loadFailed') }}</p>
      <ul v-if="ids.length" class="mt-2 flex flex-wrap gap-2" :aria-label="label">
        <li v-for="id in ids" :key="id" class="flex min-w-0 max-w-full items-center gap-1 rounded-md bg-gray-100 py-1 pl-2.5 pr-1 text-sm text-gray-800 dark:bg-dark-700 dark:text-gray-100">
          <span class="min-w-0 break-words">{{ groupName(id) }}</span>
          <span v-if="fallbackGroupIds.has(id)" class="rounded bg-amber-100 px-1.5 text-xs text-amber-900 dark:bg-amber-900/40 dark:text-amber-200">{{ t('admin.cacheSettings.groupPicker.fallbackTag') }}</span>
          <button
            type="button"
            class="shrink-0 rounded p-1 text-gray-600 hover:bg-gray-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:text-dark-300 dark:hover:bg-dark-600"
            :disabled="disabled"
            :aria-label="t('admin.cacheSettings.groupPicker.remove', { name: groupName(id) })"
            @click="removeId(id)"
          >
            <Icon name="x" size="sm" />
          </button>
        </li>
      </ul>
    </div>

    <div v-else-if="kind === 'id_list'" class="w-full max-w-xl">
      <div class="flex gap-2">
        <input
          :id="controlId"
          v-model="idInput"
          type="text"
          inputmode="numeric"
          class="input w-56 tabular-nums"
          :class="idInputError ? 'input-error' : ''"
          :placeholder="t('admin.cacheSettings.ids.placeholder')"
          :disabled="disabled"
          :aria-label="label"
          :aria-describedby="describedBy"
          @keydown.enter.prevent="commitIdInput"
        />
        <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled || !idInput.trim()" @click="commitIdInput">
          {{ t('admin.cacheSettings.ids.add') }}
        </button>
      </div>
      <p v-if="idInputError" class="input-error-text" role="alert">{{ t('admin.cacheSettings.ids.invalid') }}</p>
      <p v-else-if="!ids.length" class="input-hint">{{ t('admin.cacheSettings.ids.emptyMeansAll') }}</p>
      <ul v-if="ids.length" class="mt-2 flex flex-wrap gap-2" :aria-label="label">
        <li v-for="id in ids" :key="id" class="flex items-center gap-1 rounded-md bg-gray-100 py-1 pl-2.5 pr-1 text-sm tabular-nums text-gray-800 dark:bg-dark-700 dark:text-gray-100">
          {{ t('admin.cacheSettings.ids.item', { id }) }}
          <button
            type="button"
            class="rounded p-1 text-gray-600 hover:bg-gray-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:text-dark-300 dark:hover:bg-dark-600"
            :disabled="disabled"
            :aria-label="t('admin.cacheSettings.ids.remove', { id })"
            @click="removeId(id)"
          >
            <Icon name="x" size="sm" />
          </button>
        </li>
      </ul>
    </div>

    <input
      v-else-if="kind === 'string'"
      :id="controlId"
      type="text"
      class="input w-full font-mono sm:w-72"
      :class="invalid ? 'input-error' : ''"
      :value="String(value ?? '')"
      :disabled="disabled"
      :aria-label="label"
      :aria-describedby="describedBy"
      :aria-invalid="invalid || undefined"
      spellcheck="false"
      @change="emit('update', ($event.target as HTMLInputElement).value.trim())"
    />

    <ThresholdRulesEditor
      v-else-if="kind === 'threshold_rules'"
      :value="value"
      :allowed="field.options?.map(Number).filter((n) => Number.isFinite(n))"
      :disabled="disabled"
      :label="label"
      @update="emit('update', $event)"
    />

    <div v-else data-test="unknown-kind">
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.unknownType', { type: field.type }) }}</p>
      <pre class="mt-1 max-h-40 overflow-auto rounded-lg bg-gray-50 p-2 text-xs text-gray-700 dark:bg-dark-900 dark:text-dark-200">{{ JSON.stringify(value, null, 2) }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminGroup } from '@/types'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { codeLabel } from '../format'
import { fieldKind, fieldOptions, idRef } from '../schema'
import type { SchemaField } from '../types'
import ThresholdRulesEditor from './ThresholdRulesEditor.vue'

const props = defineProps<{
  field: SchemaField
  value: unknown
  label: string
  controlId: string
  disabled: boolean
  invalid: boolean
  describedBy?: string
  groups: AdminGroup[]
  groupsFailed: boolean
  fallbackGroupIds: Set<number>
}>()
const emit = defineEmits<{ (e: 'update', value: unknown): void }>()
const { t, te } = useI18n()

const kind = computed(() => fieldKind(props.field))
const refKind = computed(() => idRef(props.field))
const options = computed(() => fieldOptions(props.field))
const ids = computed(() => (Array.isArray(props.value) ? props.value.filter((v): v is number => typeof v === 'number') : []))
const rangeText = computed(() => {
  const { min, max, cap_source: cap } = props.field
  if (min === undefined && max === undefined) return ''
  const range = t('admin.cacheSettings.range', { min: min ?? '-', max: max ?? '-' })
  return cap ? `${range}${t('admin.cacheSettings.capSource', { source: cap })}` : range
})
const enumOptions = computed(() => {
  const list = [...options.value]
  const current = props.value
  if (typeof current === 'string' && current && !list.includes(current)) list.push(current)
  return list.map((value) => ({ value, label: codeLabel('options', value, t, te) }))
})
const groupOptions = computed(() =>
  props.groups
    .filter((g) => !ids.value.includes(g.id))
    .map((g) => ({
      value: g.id,
      label: `${g.name} (#${g.id})${props.fallbackGroupIds.has(g.id) ? ` ${t('admin.cacheSettings.groupPicker.fallbackTag')}` : ''}`,
    })),
)

function groupName(id: number): string {
  const group = props.groups.find((g) => g.id === id)
  return group ? `${group.name} (#${id})` : t('admin.cacheSettings.groupPicker.groupID', { id })
}

function onNumber(raw: string) {
  const trimmed = raw.trim()
  emit('update', trimmed === '' ? null : Number(trimmed))
}

function toggleOption(option: string, checked: boolean) {
  const current = Array.isArray(props.value) ? props.value.map(String) : []
  const next = checked ? [...new Set([...current, option])] : current.filter((v) => v !== option)
  // Keep the schema order so the diff never shows a pure reordering.
  emit('update', options.value.filter((o) => next.includes(o)).concat(next.filter((v) => !options.value.includes(v))))
}

function addId(value: string | number | boolean | null) {
  if (typeof value !== 'number' || ids.value.includes(value)) return
  emit('update', [...ids.value, value].sort((a, b) => a - b))
}

function removeId(id: number) {
  emit('update', ids.value.filter((v) => v !== id))
}

const idInput = ref('')
const idInputError = ref(false)
function commitIdInput() {
  const raw = idInput.value.trim()
  if (!raw) return
  const parsed = raw.split(/[\s,，]+/).filter(Boolean).map(Number)
  if (!parsed.every((n) => Number.isSafeInteger(n) && n > 0)) {
    idInputError.value = true
    return
  }
  idInputError.value = false
  idInput.value = ''
  emit('update', [...new Set([...ids.value, ...parsed])].sort((a, b) => a - b))
}
</script>
