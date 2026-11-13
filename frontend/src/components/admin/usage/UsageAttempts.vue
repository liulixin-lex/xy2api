<template>
  <div class="bg-gray-50 px-6 py-3 dark:bg-dark-800/60" data-usage-attempts>
    <span v-if="state === 'loading'" class="skeleton block h-3.5 w-64" />
    <span v-else-if="state === 'failed'" class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
    <table v-else class="sq-table w-auto min-w-[520px]">
      <thead>
        <tr>
          <th scope="col" class="sq-r">{{ t('admin.schedQuality.col.rank') }}</th>
          <th scope="col">{{ t('admin.schedQuality.col.account') }}</th>
          <th scope="col">{{ t('admin.schedQuality.col.result') }}</th>
          <th scope="col">{{ t('admin.schedQuality.usage.col.error') }}</th>
          <th scope="col" class="sq-r">{{ t('admin.schedQuality.usage.col.firstToken') }}</th>
          <th scope="col" class="sq-r">{{ t('admin.schedQuality.usage.col.duration') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(attempt, index) in attempts" :key="attempt.attempt_id" :data-usage-attempt="attempt.attempt_id">
          <td class="sq-r sq-num sq-text-3">{{ index + 1 }}</td>
          <td>
            <span class="inline-flex items-center gap-1.5">
              {{ attempt.account_name ?? `#${attempt.account_id}` }}
              <span v-if="attempt.fallback" class="sq-text-3 text-xs">{{ t('admin.schedQuality.usage.fallback') }}</span>
              <span v-if="attempt.takeover" class="sq-text-3 text-xs">{{ t('admin.schedQuality.usage.takeover') }}</span>
            </span>
          </td>
          <td :class="attempt.outcome === 1 ? 'sq-text' : attempt.outcome === 5 ? 'sq-text-3' : 'sq-cool'">{{ outcomeWord(attempt.outcome, t) }}</td>
          <td class="sq-text-2">{{ errorClassWord(attempt.error_class, t, te) }}</td>
          <td class="sq-r sq-num">{{ fmtSeconds(attempt.ttfo_ms) }}</td>
          <td class="sq-r sq-num">{{ fmtSeconds(attempt.duration_ms) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import adminUsageAPI, { type UsageAttempt } from '@/api/admin/usage'
import { fmtSeconds } from '@/utils/schedQualityFormat'
import { errorClassWord, outcomeWord } from '@/utils/usageResult'

// The attempts of one request (FINAL §6: 行展开看试过的每个号): account, outcome, error class, upstream first output
// and duration, in the order they were sent. Loaded once when the row opens.
const props = defineProps<{ gatewayRequestId: string; at: string }>()
const { t, te } = useI18n()
const attempts = shallowRef<UsageAttempt[]>([])
const state = ref<'loading' | 'loaded' | 'failed'>('loading')

onMounted(async () => {
  try {
    attempts.value = await adminUsageAPI.listAttempts(props.gatewayRequestId, props.at)
    state.value = 'loaded'
  } catch {
    state.value = 'failed'
  }
})
</script>
