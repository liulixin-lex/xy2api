<template>
  <BaseDialog :show="show" :title="t('keys.ccsImport.title')" width="normal" @close="emit('close')">
    <div v-if="apiKey" class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('keys.ccsImport.description') }}</p>

      <div v-if="isAntigravity">
        <span class="input-label">{{ t('keys.ccsClientSelect.description') }}</span>
        <!-- Native radios: arrow keys and a single Tab stop come from the browser. -->
        <div class="grid grid-cols-2 gap-3" role="radiogroup" :aria-label="t('keys.ccsClientSelect.title')">
          <label
            v-for="option in clientOptions"
            :key="option.value"
            :class="[
              'flex cursor-pointer flex-col items-center gap-1 rounded-xl border-2 p-3 text-center transition-colors',
              'has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-primary-500/40',
              clientType === option.value
                ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                : 'border-gray-200 hover:border-primary-300 dark:border-dark-600 dark:hover:border-primary-500'
            ]"
          >
            <input
              v-model="clientType"
              type="radio"
              name="ccs-import-client"
              :value="option.value"
              class="sr-only"
              :data-test="`ccs-client-${option.value}`"
            />
            <Icon :name="option.icon" size="lg" class="text-gray-600 dark:text-gray-400" />
            <span class="text-sm font-medium text-gray-900 dark:text-white">{{ option.label }}</span>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ option.description }}</span>
          </label>
        </div>
      </div>

      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-xl bg-gray-50 px-4 py-3 text-sm dark:bg-dark-700/50">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('keys.ccsImport.app') }}</dt>
        <dd class="font-medium text-gray-900 dark:text-white" data-test="ccs-app">{{ appLabel }}</dd>
        <dt class="text-gray-500 dark:text-gray-400">{{ t('keys.ccsImport.apiKey') }}</dt>
        <dd class="font-mono text-gray-900 dark:text-white">{{ maskedKey }}</dd>
      </dl>

      <div>
        <label for="ccs-import-name" class="input-label">{{ t('keys.ccsImport.providerName') }}</label>
        <input
          id="ccs-import-name"
          v-model="providerName"
          type="text"
          maxlength="100"
          :class="['input', nameInvalid && 'input-error']"
          data-test="ccs-name"
        />
        <p v-if="nameInvalid" class="input-error-text">{{ t('keys.ccsImport.nameRequired') }}</p>
        <p v-else class="input-hint">{{ t('keys.ccsImport.providerNameHint') }}</p>
      </div>

      <div>
        <label for="ccs-import-endpoint" class="input-label">{{ t('keys.ccsImport.endpoint') }}</label>
        <input
          id="ccs-import-endpoint"
          v-model="endpoint"
          type="url"
          inputmode="url"
          spellcheck="false"
          :class="['input font-mono', endpointInvalid && 'input-error']"
          data-test="ccs-endpoint"
        />
        <div v-if="endpointChoices.length > 1" class="mt-2 flex flex-wrap gap-2">
          <button
            v-for="choice in endpointChoices"
            :key="choice.endpoint"
            type="button"
            :title="choice.endpoint"
            :class="[
              'rounded-lg border px-2.5 py-1 text-xs transition-colors',
              endpoint.trim() === choice.endpoint
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
                : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-gray-300'
            ]"
            data-test="ccs-endpoint-choice"
            @click="endpoint = choice.endpoint"
          >
            {{ choice.label }}
          </button>
        </div>
        <p v-if="endpointInvalid" class="input-error-text">{{ t('keys.ccsImport.endpointInvalid') }}</p>
      </div>

      <div v-if="defaultModel">
        <label for="ccs-import-model" class="input-label">{{ t('keys.ccsImport.model') }}</label>
        <input
          id="ccs-import-model"
          v-model="model"
          type="text"
          spellcheck="false"
          :class="['input font-mono', modelInvalid && 'input-error']"
          data-test="ccs-model"
        />
        <p v-if="modelInvalid" class="input-error-text">{{ t('keys.ccsImport.modelRequired') }}</p>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!canImport"
          data-test="ccs-import-confirm"
          @click="confirmImport"
        >
          {{ t('keys.ccsImport.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { ApiKey, CustomEndpoint } from '@/types'
import { maskApiKey } from '@/utils/maskApiKey'
import {
  CC_SWITCH_APP_LABELS,
  defaultCcSwitchProviderName,
  isValidCcSwitchEndpoint,
  resolveCcSwitchImportConfig,
  type CcSwitchClientType,
  type CcSwitchImportOptions
} from '@/utils/ccswitchImport'

const props = withDefaults(defineProps<{
  show: boolean
  apiKey: ApiKey | null
  baseUrl: string
  siteName: string
  customEndpoints?: CustomEndpoint[]
}>(), {
  customEndpoints: () => []
})

const emit = defineEmits<{
  close: []
  import: [options: CcSwitchImportOptions]
}>()

const { t } = useI18n()

const clientType = ref<CcSwitchClientType>('claude')
const providerName = ref('')
const endpoint = ref('')
const model = ref('')

const platform = computed(() => props.apiKey?.group?.platform || 'anthropic')
const isAntigravity = computed(() => platform.value === 'antigravity')
const importConfig = computed(() =>
  resolveCcSwitchImportConfig(platform.value, clientType.value, props.baseUrl)
)
const appLabel = computed(() => CC_SWITCH_APP_LABELS[importConfig.value.app] ?? importConfig.value.app)
// Only apps that already carry a default model (Codex, Grok Build) get a model field.
const defaultModel = computed(() => importConfig.value.model ?? '')
const maskedKey = computed(() => maskApiKey(props.apiKey?.key ?? ''))

const clientOptions = computed(() => [
  {
    value: 'claude' as const,
    icon: 'terminal' as const,
    label: t('keys.ccsClientSelect.claudeCode'),
    description: t('keys.ccsClientSelect.claudeCodeDesc')
  },
  {
    value: 'gemini' as const,
    icon: 'sparkles' as const,
    label: t('keys.ccsClientSelect.geminiCli'),
    description: t('keys.ccsClientSelect.geminiCliDesc')
  }
])

// Quick picks for the site address and each admin-configured endpoint, already
// shaped the way the selected app expects (for example, /v1 for Grok Build).
const endpointChoices = computed(() => {
  const bases = [
    { label: t('keys.ccsImport.endpointDefault'), base: props.baseUrl },
    ...props.customEndpoints
      .filter((item) => item.endpoint?.trim())
      .map((item) => ({ label: item.name?.trim() || item.endpoint.trim(), base: item.endpoint.trim() }))
  ]
  const seen = new Set<string>()
  return bases
    .map(({ label, base }) => ({
      label,
      endpoint: resolveCcSwitchImportConfig(platform.value, clientType.value, base).endpoint
    }))
    .filter((choice) => {
      if (seen.has(choice.endpoint)) return false
      seen.add(choice.endpoint)
      return true
    })
})

const nameInvalid = computed(() => providerName.value.trim() === '')
const endpointInvalid = computed(() => !isValidCcSwitchEndpoint(endpoint.value))
const modelInvalid = computed(() => defaultModel.value !== '' && model.value.trim() === '')
const canImport = computed(
  () => props.apiKey !== null && !nameInvalid.value && !endpointInvalid.value && !modelInvalid.value
)

const defaultName = computed(() => defaultCcSwitchProviderName(props.siteName, props.apiKey?.name))
const defaultEndpoint = computed(() => importConfig.value.endpoint)

function resetForm() {
  clientType.value = platform.value === 'gemini' ? 'gemini' : 'claude'
  providerName.value = defaultName.value
  endpoint.value = defaultEndpoint.value
  model.value = defaultModel.value
}

watch(
  () => [props.show, props.apiKey?.id] as const,
  ([show]) => {
    if (show) resetForm()
  },
  { immediate: true }
)

// The site name and address come from public settings, which can arrive after
// the panel opens. Fields the user has not edited follow the new defaults;
// edited ones stay as typed.
watch(defaultName, (next, prev) => {
  if (props.show && providerName.value === prev) providerName.value = next
})
watch(defaultEndpoint, (next, prev) => {
  if (props.show && endpoint.value === prev) endpoint.value = next
})

function confirmImport() {
  if (!canImport.value) return
  emit('import', {
    clientType: clientType.value,
    providerName: providerName.value.trim(),
    endpoint: endpoint.value.trim(),
    model: defaultModel.value ? model.value.trim() : undefined
  })
}
</script>
