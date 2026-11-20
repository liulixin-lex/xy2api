<template>
  <section class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-400">
    <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groups.openaiFast.title') }}</h4>
    <p class="input-hint">{{ t('admin.groups.openaiFast.scope') }}</p>
    <label v-if="legacy" class="mt-3 flex items-start gap-2 [@media(any-pointer:coarse)]:min-h-10 text-sm text-gray-700 dark:text-gray-300">
      <input v-model="migrate" type="checkbox" class="mt-1" :disabled="disabled" data-testid="migrate-openai-tier" />
      <span>
        {{ t('admin.groups.openaiFast.migrate') }}
        <span class="input-hint block">{{ t('admin.groups.openaiFast.legacy') }}</span>
      </span>
    </label>
    <div v-for="tier in tiers" :key="tier.mode" class="mt-3 grid gap-x-4 gap-y-2 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,auto)] sm:items-end">
      <label class="block min-w-0">
        <span class="input-label">{{ tier.label === 'Fast' ? 'Fast (priority)' : tier.label }}</span>
        <select
          class="input"
          :value="model[tier.mode]"
          :disabled="locked"
          :data-testid="tier.mode"
          @change="setMode(tier.mode, ($event.target as HTMLSelectElement).value as OpenAITierMode)"
        >
          <option value="inherit">{{ t('admin.groups.openaiFast.inherit') }}</option>
          <option value="force_on" :disabled="model[tier.other] === 'force_on'">{{ t('admin.groups.openaiFast.forceOn') }}</option>
          <option value="force_off">{{ t('admin.groups.openaiFast.forceOff') }}</option>
        </select>
      </label>
      <label class="flex items-center gap-2 [@media(any-pointer:coarse)]:min-h-10 text-sm text-gray-700 dark:text-gray-300 sm:pb-2">
        <input type="checkbox" :checked="model[tier.free]" :disabled="locked" :data-testid="tier.free"
          @change="model = { ...model, [tier.free]: ($event.target as HTMLInputElement).checked }" />
        {{ t('admin.groups.openaiFast.waive', { tier: tier.label }) }}
      </label>
    </div>
    <p class="input-hint mt-3">{{ t('admin.groups.openaiFast.billing') }}</p>
    <p v-if="fallback" class="input-hint">{{ t('admin.groups.openaiFast.fallback') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAITierMode } from '@/types'
import type { GroupTierPolicy } from '@/views/admin/groupsOpenAIFast'

const { t } = useI18n()
const props = defineProps<{ legacy?: boolean; disabled?: boolean; fallback?: boolean }>()
const model = defineModel<GroupTierPolicy>({ required: true })
const migrate = defineModel<boolean>('migrate', { default: false })
const locked = computed(() => props.disabled || (props.legacy && !migrate.value))
const tiers = [
  { label: 'Fast', mode: 'openai_fast_mode', other: 'openai_ultrafast_mode', free: 'free_openai_fast' },
  { label: 'Ultrafast', mode: 'openai_ultrafast_mode', other: 'openai_fast_mode', free: 'free_openai_ultrafast' },
] as const

function setMode(key: typeof tiers[number]['mode'], value: OpenAITierMode) {
  const other = key === 'openai_fast_mode' ? 'openai_ultrafast_mode' : 'openai_fast_mode'
  if (locked.value || (value === 'force_on' && model.value[other] === 'force_on')) return
  model.value = { ...model.value, [key]: value }
}
</script>
