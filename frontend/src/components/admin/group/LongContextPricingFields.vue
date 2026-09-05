<template>
  <div class="ml-6 mt-3 space-y-3">
    <div>
      <span class="input-label">{{ t('admin.groups.modelPricing.longContextScope') }}</span>
      <div class="inline-flex overflow-hidden rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-dark-600 dark:bg-dark-800">
        <button
          v-for="option in scopeOptions"
          :key="option.value"
          type="button"
          class="min-w-24 rounded-md px-3 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
          :class="scope === option.value
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
            : 'text-gray-500 hover:text-gray-800 dark:text-dark-300 dark:hover:text-white'"
          :aria-pressed="scope === option.value"
          @click="emit('update:scope', option.value)"
        >
          {{ option.label }}
        </button>
      </div>
    </div>

    <div v-if="scope === 'selected'" ref="selectorRef" class="relative">
      <label class="input-label" :for="inputId">
        {{ t('admin.groups.modelPricing.longContextModels') }}
      </label>
      <div
        class="flex min-h-10 flex-wrap items-center gap-1.5 rounded-lg border border-gray-200 bg-white p-2 focus-within:border-primary-500 focus-within:ring-1 focus-within:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
      >
        <span
          v-for="model in models"
          :key="model"
          class="inline-flex max-w-full items-center gap-1 rounded-md bg-primary-50 px-2 py-0.5 text-sm text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
        >
          <span class="truncate">{{ model }}</span>
          <button
            type="button"
            class="shrink-0 rounded p-0.5 hover:bg-primary-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:hover:bg-primary-900/50"
            :aria-label="t('admin.groups.modelPricing.removeLongContextModel', { model })"
            @click="removeModel(model)"
          >
            <Icon name="x" size="xs" />
          </button>
        </span>
        <input
          :id="inputId"
          v-model="query"
          type="text"
          role="combobox"
          class="min-w-40 flex-1 border-0 bg-transparent p-0 text-sm text-gray-900 outline-none placeholder:text-gray-400 focus:ring-0 dark:text-white"
          :placeholder="models.length === 0 ? t('admin.groups.modelPricing.longContextModelsPlaceholder') : ''"
          :aria-expanded="dropdownOpen"
          autocomplete="off"
          @focus="dropdownOpen = true"
          @keydown.enter.prevent="addQuery"
          @keydown.escape="dropdownOpen = false"
          @keydown.backspace="removeLastWhenEmpty"
        />
      </div>

      <div
        v-if="dropdownOpen && (filteredCandidates.length > 0 || loading)"
        class="absolute z-20 mt-1 max-h-52 w-full overflow-y-auto rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-700"
      >
        <div v-if="loading" class="px-3 py-2 text-sm text-gray-500 dark:text-dark-300">
          {{ t('common.loading') }}
        </div>
        <template v-else>
          <button
            v-for="model in filteredCandidates"
            :key="model"
            type="button"
            class="block w-full truncate px-3 py-2 text-left text-sm text-gray-700 hover:bg-gray-50 focus-visible:bg-gray-50 focus-visible:outline-none dark:text-gray-200 dark:hover:bg-dark-600 dark:focus-visible:bg-dark-600"
            @mousedown.prevent="addModel(model)"
          >
            {{ model }}
          </button>
        </template>
      </div>
      <p class="input-hint">
        {{ t('admin.groups.modelPricing.longContextModelsHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { onClickOutside } from '@vueuse/core'
import Icon from '@/components/icons/Icon.vue'
import type { LongContextPricingScope } from '@/types'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  scope: LongContextPricingScope
  models: string[]
  candidates?: string[]
  loading?: boolean
  inputId: string
}>(), {
  candidates: () => [],
  loading: false
})

const emit = defineEmits<{
  'update:scope': [value: LongContextPricingScope]
  'update:models': [value: string[]]
}>()

const { t } = useI18n()
const query = ref('')
const dropdownOpen = ref(false)
const selectorRef = ref<HTMLElement | null>(null)

const scopeOptions = computed(() => [
  { value: 'selected' as const, label: t('admin.groups.modelPricing.longContextScopeSelected') },
  { value: 'all' as const, label: t('admin.groups.modelPricing.longContextScopeAll') }
])

const filteredCandidates = computed(() => {
  const selected = new Set(props.models.map(model => model.toLowerCase()))
  const needle = query.value.trim().toLowerCase()
  return props.candidates.filter(model =>
    !selected.has(model.toLowerCase()) && (!needle || model.toLowerCase().includes(needle))
  )
})

onClickOutside(selectorRef, () => {
  dropdownOpen.value = false
})

function addModel(raw: string) {
  const model = raw.trim()
  if (!model || props.models.some(item => item.toLowerCase() === model.toLowerCase())) return
  emit('update:models', [...props.models, model])
  query.value = ''
  dropdownOpen.value = true
}

function addQuery() {
  addModel(query.value)
}

function removeModel(model: string) {
  emit('update:models', props.models.filter(item => item !== model))
}

function removeLastWhenEmpty() {
  if (query.value || props.models.length === 0) return
  emit('update:models', props.models.slice(0, -1))
}
</script>
