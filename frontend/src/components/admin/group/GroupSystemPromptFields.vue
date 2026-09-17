<template>
  <section class="space-y-4 border-t border-gray-200 pt-5 dark:border-dark-600" :aria-labelledby="`${idPrefix}-title`">
    <h3 :id="`${idPrefix}-title`" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.groups.systemPrompt.title') }}</h3>
    <div>
      <label :for="`${idPrefix}-prompt`" class="input-label">{{ t('admin.groups.systemPrompt.common') }}</label>
      <textarea :id="`${idPrefix}-prompt`" v-model="draft.prompt" rows="5" class="input resize-y" />
    </div>
    <div class="flex flex-wrap gap-1" role="group" :aria-label="t('admin.groups.systemPrompt.scope')">
      <button v-for="scope in scopes" :key="scope" type="button" class="rounded-md border px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500" :class="draft.scope === scope ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200' : 'border-gray-200 text-gray-700 dark:border-dark-600 dark:text-gray-200'" :aria-pressed="draft.scope === scope" @click="draft.scope = scope">
        {{ t(`admin.groups.systemPrompt.${scope}`) }}
      </button>
    </div>
    <div v-if="draft.scope === 'selected'" class="space-y-2">
      <label :for="`${idPrefix}-models`" class="input-label">{{ t('admin.groups.systemPrompt.models') }}</label>
      <Select :id="`${idPrefix}-models`" :model-value="null" :options="availableOptions" searchable creatable :loading="loading" :placeholder="t('admin.groups.systemPrompt.modelPlaceholder')" @update:model-value="addModel" />
      <div class="flex flex-wrap gap-2">
        <span v-for="model in draft.models" :key="model" class="inline-flex max-w-full items-center gap-1 rounded-md bg-gray-100 px-2 py-1 text-sm text-gray-800 dark:bg-dark-700 dark:text-gray-100">
          <span class="break-all">{{ model }}</span>
          <button type="button" class="shrink-0 rounded p-1 focus-visible:ring-2 focus-visible:ring-primary-500" :title="t('common.remove')" :aria-label="`${t('common.remove')} ${model}`" @click="draft.models = draft.models.filter(item => item !== model)"><Icon name="x" size="xs" /></button>
        </span>
      </div>
    </div>
    <div class="flex flex-wrap items-center justify-between gap-2 pt-1">
      <h4 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.groups.systemPrompt.overrides') }}</h4>
      <button type="button" class="btn btn-secondary btn-sm" @click="addOverride"><Icon name="plus" size="sm" class="mr-1" />{{ t('admin.groups.systemPrompt.addModel') }}</button>
    </div>
    <div v-for="(row, index) in rows" :key="row.id" class="space-y-2 border-t border-gray-100 pt-3 dark:border-dark-700">
      <label :for="`${idPrefix}-model-${row.id}`" class="input-label">{{ t('admin.groups.systemPrompt.model') }}</label>
      <div class="flex min-w-0 items-start gap-2">
        <Select :id="`${idPrefix}-model-${row.id}`" v-model="row.model" class="min-w-0 flex-1" :options="modelOptions" searchable creatable :loading="loading" :placeholder="t('admin.groups.systemPrompt.modelPlaceholder')" />
        <button type="button" class="btn btn-secondary h-10 w-10 shrink-0 !p-0" :title="t('common.remove')" :aria-label="`${t('common.remove')} ${row.model || index + 1}`" @click="rows.splice(index, 1)"><Icon name="trash" size="sm" /></button>
      </div>
      <label :for="`${idPrefix}-override-${row.id}`" class="sr-only">{{ t('admin.groups.systemPrompt.modelPrompt') }}</label>
      <textarea :id="`${idPrefix}-override-${row.id}`" v-model="row.prompt" rows="4" class="input resize-y" />
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupSystemPromptConfig } from '@/types'
import { cloneGroupSystemPromptConfig } from '@/utils/groupSystemPrompt'

const props = withDefaults(defineProps<{ modelValue: GroupSystemPromptConfig; idPrefix: string; candidates?: string[]; loading?: boolean }>(), { candidates: () => [], loading: false })
const emit = defineEmits<{ 'update:modelValue': [value: GroupSystemPromptConfig] }>()
const { t } = useI18n()
const scopes = ['all', 'selected'] as const
const draft = ref(cloneGroupSystemPromptConfig(props.modelValue))
let nextID = 0
const rows = ref<Array<{ id: number; model: string; prompt: string }>>([])
const error = ref('')
watch(() => props.modelValue, value => {
  draft.value = cloneGroupSystemPromptConfig(value)
  rows.value = Object.entries(value.model_prompts).map(([model, prompt]) => ({ id: nextID++, model, prompt }))
  error.value = ''
}, { immediate: true })
const modelOptions = computed(() => [...new Set([...props.candidates, ...draft.value.models, ...rows.value.map(row => row.model).filter(Boolean)])].map(value => ({ value, label: value })))
const availableOptions = computed(() => modelOptions.value.filter(item => !draft.value.models.includes(item.value)))
function addModel(value: string | number | boolean | null | undefined) {
  if (typeof value !== 'string' || !value.trim()) return
  const model = value.trim()
  if (!draft.value.models.includes(model)) draft.value.models.push(model)
}
function addOverride() { rows.value.push({ id: nextID++, model: '', prompt: '' }) }
function validate() {
  error.value = ''
  if (draft.value.prompt.trim() && draft.value.scope === 'selected' && !draft.value.models.length) {
    error.value = t('admin.groups.systemPrompt.selectModelsError')
    return false
  }
  const seen = new Set<string>()
  const entries: Array<[string, string]> = []
  for (const row of rows.value) {
    const model = row.model.trim()
    if (!model) { error.value = t('admin.groups.systemPrompt.modelRequired'); return false }
    if (seen.has(model)) { error.value = t('admin.groups.systemPrompt.duplicateModel', { model }); return false }
    seen.add(model)
    if (row.prompt.trim()) entries.push([model, row.prompt])
  }
  emit('update:modelValue', { ...cloneGroupSystemPromptConfig(draft.value), model_prompts: Object.fromEntries(entries) })
  return true
}
defineExpose({ validate })
</script>
