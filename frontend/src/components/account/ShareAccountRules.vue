<template>
  <div v-if="rules.length" class="mt-2 rounded-lg border border-gray-200 p-3 text-xs dark:border-dark-600" data-testid="share-account-rules">
    <span class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.shareScheduling.accountRules.title') }}</span>
    <ul class="mt-1 space-y-1">
      <li v-for="rule in rules" :key="rule.group_id" class="flex flex-wrap items-center gap-x-3 text-gray-600 dark:text-dark-300" :data-testid="'share-account-rule-' + rule.group_id">
        <span class="font-medium text-gray-800 dark:text-gray-100">{{ rule.group_name || `#${rule.group_id}` }}</span>
        <span v-if="!rule.effective" class="text-gray-400 dark:text-dark-400">{{ t('admin.shareScheduling.accountRules.notEffective') }}</span>
        <span>{{ t('admin.shareScheduling.accountRules.share', { value: formatSharePercent(rule.expected_share) }) }}</span>
        <span v-if="!rule.healthy && rule.share_if_healthy > 0" class="text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.traffic.ifHealthy', { value: formatSharePercent(rule.share_if_healthy) }) }}</span>
        <router-link :to="{ name: 'AdminShareScheduling', query: { group: String(rule.group_id) } }" class="text-primary-600 hover:underline dark:text-primary-400">{{ t('admin.shareScheduling.accountRules.open') }}</router-link>
      </li>
    </ul>
    <p class="mt-1 text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.accountRules.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type { ShareAccountGroupRule } from '@/api/admin/shareScheduling'
import { formatSharePercent } from '@/utils/shareScheduling'

/**
 * The account's expected share of new sessions in each share group. It
 * renders nothing when no group runs share scheduling or the data cannot be
 * loaded, so the account editor never depends on it.
 */
const props = defineProps<{ accountId: number }>()

const { t } = useI18n()
const rules = ref<ShareAccountGroupRule[]>([])
let request = 0

async function load(accountId: number) {
  const current = ++request
  rules.value = []
  if (!accountId) return
  try {
    const loaded = await shareSchedulingAPI.accountRules(accountId)
    if (current === request) rules.value = loaded
  } catch {
    // The editor works without this hint.
  }
}

watch(() => props.accountId, id => { void load(id) }, { immediate: true })
</script>
