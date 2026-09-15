<template>
  <div v-if="account.platform === 'openai'" class="inline-flex min-h-8 items-center gap-2 whitespace-nowrap" data-testid="iq-check-cell">
    <button type="button" role="switch" :aria-checked="!!state?.enabled" :aria-label="t('admin.accounts.iqCheck')" :title="t('admin.accounts.iqCheck')" :disabled="busy" class="relative inline-flex h-5 w-9 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-50" :class="state?.enabled ? 'bg-blue-500' : 'bg-gray-200 dark:bg-dark-600'" @click="$emit('toggle')">
      <span class="pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow transition-transform" :class="state?.enabled ? 'translate-x-4' : 'translate-x-0'" />
    </button>
    <button type="button" class="inline-flex min-h-8 items-center gap-1.5 rounded px-1 text-xs hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 dark:hover:bg-dark-700" :class="statusClass" :title="t('admin.accounts.iqRecords')" :aria-label="t('admin.accounts.iqOpenRecords', { status: statusLabel })" @click="$emit('records')">
      <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-current" aria-hidden="true" />
      <span>{{ statusLabel }}</span>
      <Icon v-if="state?.enabled && state.execution_state === 'running'" name="refresh" size="xs" class="animate-spin" :title="t('admin.accounts.iqRunning')" />
      <Icon v-else-if="state?.enabled && state.execution_state === 'paused'" name="exclamationCircle" size="xs" :title="t('admin.accounts.iqExecutionStates.paused')" />
      <Icon v-else-if="state?.enabled && state.freshness === 'stale'" name="clock" size="xs" :title="t('admin.accounts.iqFreshness.stale')" />
    </button>
  </div>
  <span v-else class="text-xs text-gray-400">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Account } from '@/types'

const props = defineProps<{ account: Account; busy?: boolean }>()
defineEmits<{ toggle: []; records: [] }>()
const { t } = useI18n()
const state = computed(() => props.account.iq_check)
const status = computed(() => state.value?.enabled ? state.value.status : 'off')
const statusLabel = computed(() => t(status.value === 'off' ? 'admin.accounts.iqOff' : `admin.accounts.iqCheckStatus.${status.value || 'unknown'}`))
const statusClass = computed(() => status.value === 'smart' ? 'text-green-700 dark:text-green-400' : status.value === 'degraded' ? 'text-amber-700 dark:text-amber-400' : 'text-gray-600 dark:text-dark-300')
</script>
