<template>
  <div class="space-y-4" data-testid="resilience-explain-result">
    <dl class="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
      <div v-for="item in summary" :key="item.key" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.resilience.explain.summary.' + item.key) }}</dt>
        <dd class="mt-1 break-all font-medium text-gray-900 dark:text-white" :data-testid="'resilience-explain-' + item.key">{{ item.value }}</dd>
      </div>
    </dl>
    <div class="overflow-x-auto">
      <table class="w-full whitespace-nowrap text-left text-xs" data-testid="resilience-explain-accounts">
        <thead>
          <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-600 dark:text-dark-400">
            <th class="p-2">{{ t('admin.resilience.explain.columns.account') }}</th>
            <th class="p-2 text-right">{{ t('admin.resilience.explain.columns.priority') }}</th>
            <th class="p-2">{{ t('admin.resilience.explain.columns.tier') }}</th>
            <th class="p-2">{{ t('admin.resilience.explain.columns.reason') }}</th>
            <th class="p-2">{{ t('admin.resilience.explain.columns.recoverAt') }}</th>
            <th class="p-2">{{ t('admin.resilience.explain.columns.balance') }}</th>
            <th class="p-2 text-right">{{ t('admin.resilience.explain.columns.sessionFailures') }}</th>
            <th class="p-2">{{ t('admin.resilience.explain.columns.avoided') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="acc in explanation.accounts" :key="acc.account_id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
            <td class="p-2">{{ accountLabel(acc) }}</td>
            <td class="p-2 text-right tabular-nums">{{ acc.priority }}</td>
            <td class="p-2"><span :class="['rounded-full px-2 py-0.5 font-medium', tierClass(acc.tier)]">{{ t('admin.resilience.explain.tiers.' + acc.tier) }}</span></td>
            <td class="p-2">{{ acc.reason ? reasonLabel(acc.reason) : '-' }}</td>
            <td class="p-2">{{ acc.recover_at ? formatDateTime(acc.recover_at) : '-' }}</td>
            <td class="p-2">{{ acc.balance_state ? balanceLabel(acc.balance_state) : '-' }}</td>
            <td class="p-2 text-right tabular-nums">{{ acc.session_failures || '-' }}</td>
            <td class="p-2">{{ acc.avoided ? t('admin.resilience.explain.yes') : '-' }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="!explanation.accounts.length" class="py-3 text-center text-xs text-gray-500 dark:text-dark-400">{{ t('admin.resilience.explain.noAccounts') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ResilienceAccountExplanation, ResilienceExplanation } from '@/api/admin/resilience'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ explanation: ResilienceExplanation }>()

const { t, te } = useI18n()

const names = computed(() => new Map(props.explanation.accounts.map(a => [a.account_id, a.name])))
const accountName = (id: number) => `${names.value.get(id) ?? ''} #${id}`.trim()
const accountLabel = (acc: ResilienceAccountExplanation) => `${acc.name} #${acc.account_id}`.trim()

function reasonLabel(reason: string): string {
  const key = `admin.resilience.explain.reasons.${reason}`
  return te(key) ? t(key) : reason
}

function balanceLabel(state: string): string {
  const key = `admin.resilience.panel.states.${state}`
  return te(key) ? t(key) : state
}

function tierClass(tier: string): string {
  switch (tier) {
    case 'hard': return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
    case 'soft': return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
    default: return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
  }
}

const summary = computed(() => {
  const e = props.explanation
  const none = t('admin.resilience.explain.none')
  const yes = t('admin.resilience.explain.yes')
  const no = t('admin.resilience.explain.no')
  let lastResort = none
  if (e.last_resort) {
    lastResort = accountName(e.last_resort.account_id)
    if (e.last_resort.recover_at) lastResort += ` · ${t('admin.resilience.explain.recoverAt', { time: formatDateTime(e.last_resort.recover_at) })}`
  }
  let fallback = t('admin.resilience.explain.fallbackNone')
  const fb = e.fallback_group
  if (fb) {
    const name = `${fb.name || ''} #${fb.group_id}`.trim()
    if (!fb.usable) fallback = t('admin.resilience.explain.fallbackUnusable', { group: name })
    else if (fb.would_move) fallback = t('admin.resilience.explain.fallbackMove', { group: name })
    else if (!fb.fallback_healthy) fallback = t('admin.resilience.explain.fallbackUnhealthy', { group: name })
    else fallback = t('admin.resilience.explain.fallbackStay', { group: name })
  }
  const items = [
    { key: 'enabled', value: e.enabled ? yes : no },
    { key: 'healthy', value: String(e.healthy_count) },
    { key: 'lastResort', value: lastResort },
    { key: 'fallback', value: fallback }
  ]
  if (e.session) {
    items.push(
      { key: 'sessionFailures', value: String(e.session.failures) },
      { key: 'sessionAvoided', value: e.session.avoided.length ? e.session.avoided.map(accountName).join(', ') : none }
    )
  }
  return items
})
</script>
