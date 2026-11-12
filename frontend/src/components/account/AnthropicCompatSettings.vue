<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="anthropic-compat-section">
    <div class="flex items-start justify-between gap-4">
      <div class="space-y-1">
        <label :for="`${id}-enabled`" class="input-label mb-0">{{ t('admin.accounts.anthropic.compatibility') }}</label>
        <p :id="`${id}-desc`" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.anthropic.compatibilityDesc') }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.anthropic.compatibilityHistory') }}</p>
        <p :class="['text-xs', passthrough ? 'text-amber-700 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400']">{{ t('admin.accounts.anthropic.compatibilityConflict') }}</p>
      </div>
      <select v-if="bulk" :id="`${id}-enabled`" :value="toOption(enabled)" class="input w-32 shrink-0 text-sm" data-testid="anthropic-compat-toggle" @change="enabled = fromOption($event)">
        <option v-for="option in bulkOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>
      <Toggle v-else :id="`${id}-enabled`" :model-value="enabled === true" :disabled="passthrough" :aria-describedby="`${id}-desc`" data-testid="anthropic-compat-toggle" @update:model-value="enabled = $event" />
    </div>
    <div v-if="enabled !== false" class="mt-3 flex items-start justify-between gap-4">
      <div class="space-y-1">
        <label :for="`${id}-new-sessions`" class="input-label mb-0">{{ t('admin.accounts.anthropic.compatibilityNewSessions') }}</label>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.anthropic.compatibilityNewSessionsDesc') }}</p>
      </div>
      <select v-if="bulk" :id="`${id}-new-sessions`" :value="toOption(newSessions)" class="input w-32 shrink-0 text-sm" data-testid="anthropic-compat-new-sessions" @change="newSessions = fromOption($event)">
        <option v-for="option in bulkOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>
      <Toggle v-else :id="`${id}-new-sessions`" :model-value="newSessions !== false" data-testid="anthropic-compat-new-sessions" @update:model-value="newSessions = $event" />
    </div>
  </div>
</template>

<script setup lang="ts">
// 只用于 Anthropic API Key 账号，是否显示由弹窗决定。bulk 模式两项都是三态，null 表示不修改。
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { AnthropicCompatDraft } from './anthropicCompat'

defineProps<{ bulk?: boolean; passthrough?: boolean }>()
const model = defineModel<AnthropicCompatDraft>({ required: true })

const { t } = useI18n()
const id = useId()

const field = (key: keyof AnthropicCompatDraft) => computed({
  get: () => model.value[key],
  set: (value: boolean | null) => { model.value = { ...model.value, [key]: value } }
})
const enabled = field('enabled')
const newSessions = field('newSessions')

const bulkOptions = computed(() => [
  { value: '', label: t('admin.accounts.anthropic.compatibilityKeep') },
  { value: 'on', label: t('admin.accounts.anthropic.compatibilityOn') },
  { value: 'off', label: t('admin.accounts.anthropic.compatibilityOff') }
])
const toOption = (value: boolean | null) => (value === null ? '' : value ? 'on' : 'off')
const fromOption = (event: Event) => {
  const value = (event.target as HTMLSelectElement).value
  return value === '' ? null : value === 'on'
}
</script>
