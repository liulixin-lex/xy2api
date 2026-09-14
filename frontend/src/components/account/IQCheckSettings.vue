<template>
  <fieldset class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="flex items-center gap-2 text-sm font-medium">
      <input type="checkbox" class="rounded text-blue-500 focus:ring-blue-500" :checked="modelValue.enabled" @change="updateEnabled" />
      {{ t('admin.accounts.iqCheck') }}
    </label>
    <label class="block text-sm">
      {{ t('admin.accounts.iqInterval') }}
      <input type="number" class="input mt-1 max-w-40" min="1" max="1440" step="1" required :value="modelValue.interval_minutes" @input="updateInterval" />
    </label>
    <p class="input-hint">{{ t('admin.accounts.iqHint') }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { IQCheckSettings } from '@/types'
const props = defineProps<{ modelValue: IQCheckSettings }>()
const emit = defineEmits<{ 'update:modelValue': [value: IQCheckSettings] }>()
const { t } = useI18n()
const updateEnabled = (event: Event) => emit('update:modelValue', { ...props.modelValue, enabled: (event.target as HTMLInputElement).checked })
const updateInterval = (event: Event) => emit('update:modelValue', { ...props.modelValue, interval_minutes: Number((event.target as HTMLInputElement).value) })
</script>
