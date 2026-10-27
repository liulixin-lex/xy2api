<template>
  <div v-if="account.platform === 'openai'" class="inline-flex min-h-8 items-center gap-2 whitespace-nowrap" data-testid="iq-check-cell">
    <button type="button" role="switch" :aria-checked="enabled" :aria-label="t('admin.accounts.iqCheck')" :title="t('admin.accounts.iqToggleBuiltins')" :disabled="busy" class="relative inline-flex h-5 w-9 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-50" :class="enabled ? 'bg-blue-500' : 'bg-gray-200 dark:bg-dark-600'" @click="$emit('toggle', !enabled)">
      <span class="pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow transition-transform" :class="enabled ? 'translate-x-4' : 'translate-x-0'" />
    </button>
    <button type="button" class="inline-flex min-h-8 items-center gap-1 rounded px-1 text-xs hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 dark:hover:bg-dark-700" :title="t('admin.accounts.iqRecords')" :aria-label="t('admin.accounts.iqOpenRecords', { status: tags.map(tag => tag.text).join(', ') || t('admin.accounts.iqOff') })" @click="$emit('records')">
      <span v-if="!tags.length" class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqOff') }}</span>
      <span v-for="tag in tags" :key="tag.model" class="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5" :class="tag.tone" data-testid="iq-target-tag">
        <Icon v-if="tag.spinning" name="refresh" size="xs" class="animate-spin" />
        {{ tag.text }}
      </span>
    </button>
  </div>
  <span v-else class="text-xs text-gray-400">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Account, IQTargetSummary } from '@/types'
import { isIQBuiltinModel, iqModelLabel, sortIQTargets } from '@/components/admin/account/iqCheckPresentation'

const props = defineProps<{ account: Account; busy?: boolean }>()
// toggle carries the requested state for both built-in targets.
defineEmits<{ toggle: [enabled: boolean]; records: [] }>()
const { t } = useI18n()
const targets = computed(() => sortIQTargets(props.account.iq_targets ?? []))
const enabled = computed(() => targets.value.some(target => isIQBuiltinModel(target.model) && target.enabled))

function tag(target: IQTargetSummary) {
  const name = iqModelLabel(target.model)
  if (target.state === 'running') return { text: `${name} ${t('admin.accounts.iqStates.running')}`, tone: 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300', spinning: true }
  if (target.state === 'queued') return { text: `${name} ${t('admin.accounts.iqStates.queued')}`, tone: 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300' }
  if (target.gate_active) return { text: `${name} ${t('admin.accounts.iqCheckStatus.degraded')}·${t('admin.accounts.iqCooling')}`, tone: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-400' }
  if (target.state === 'paused') return { text: `${name} ${t('admin.accounts.iqStates.paused')}`, tone: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' }
  const tone = target.verdict === 'smart' ? 'text-emerald-700 dark:text-emerald-400' : target.verdict === 'degraded' ? 'text-red-700 dark:text-red-400' : 'text-gray-600 dark:text-dark-300'
  return { text: `${name} ${t('admin.accounts.iqCheckStatus.' + target.verdict)}`, tone }
}
// Disabled targets stay hidden unless they still hold a gate or are being checked manually.
const tags = computed(() => targets.value.filter(target => target.enabled || target.gate_active || target.state === 'queued' || target.state === 'running').map(target => ({ model: target.model, spinning: false, ...tag(target) })))
</script>
