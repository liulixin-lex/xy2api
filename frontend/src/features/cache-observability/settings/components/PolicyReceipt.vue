<template>
  <!-- Visible countdown is not a live region; the sr-only line changes only when a countdown starts or ends (m-5). -->
  <p v-if="pendingText" class="mt-3 flex items-start gap-2 text-sm text-gray-700 dark:text-dark-200" data-test="policy-pending">
    <Icon name="clock" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
    {{ pendingText }}
  </p>
  <p class="sr-only" aria-live="polite" data-test="policy-live">{{ liveText }}</p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { formatLocalClock, formatUtcClock } from '../format'
import type { PoliciesState } from '../usePolicies'

const props = defineProps<{ state: PoliciesState; policyKey: string; now: Date }>()
const { t } = useI18n()

/** After a write (receipt) or while the server reports pending_reload: when every instance has it. */
const pendingText = computed(() => {
  const receipt = props.state.receipts.value[props.policyKey]
  if (receipt?.savedOnly) return t('admin.cacheSettings.savedOnly', { rev: receipt.rev })
  if (receipt && !receipt.settled && props.now < receipt.effectiveBy) {
    return t('admin.cacheSettings.written', { rev: receipt.rev, time: formatUtcClock(receipt.effectiveBy), local: formatLocalClock(receipt.effectiveBy) })
  }
  const deadline = props.state.deadlineOf(props.policyKey)
  const e = props.state.policy(props.policyKey)
  if (e && props.state.stateOf(props.policyKey) === 'pending_reload' && deadline && !deadline.receipt) {
    const seconds = Math.ceil((deadline.at.getTime() - props.now.getTime()) / 1000)
    const params = { stored: e.stored.rev, loaded: e.loaded?.rev ?? '-', seconds }
    return seconds > 0 ? t('admin.cacheSettings.pendingReload', params) : t('admin.cacheSettings.pendingOverdue', params)
  }
  return ''
})

// Changes only when a countdown starts or ends, so assistive tech hears two messages, not one per second.
const liveText = computed(() => {
  const receipt = props.state.receipts.value[props.policyKey]
  if (receipt?.savedOnly) return t('admin.cacheSettings.savedOnly', { rev: receipt.rev })
  if (receipt) return receipt.settled ? t('admin.cacheSettings.settledLive', { rev: receipt.rev }) : t('admin.cacheSettings.writtenLive', { rev: receipt.rev })
  const e = props.state.policy(props.policyKey)
  if (e && props.state.stateOf(props.policyKey) === 'pending_reload') return t('admin.cacheSettings.pendingLive', { stored: e.stored.rev, loaded: e.loaded?.rev ?? '-' })
  return ''
})
</script>
