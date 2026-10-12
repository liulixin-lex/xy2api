<template>
  <div v-if="rules.length" class="mt-2 rounded-lg border border-gray-200 p-3 text-xs dark:border-dark-600" data-testid="share-account-rules">
    <div class="flex items-center justify-between gap-2">
      <span class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.shareScheduling.accountRules.title') }}</span>
      <router-link :to="{ name: 'AdminShareScheduling' }" class="text-primary-600 hover:underline dark:text-primary-400">{{ t('admin.shareScheduling.accountRules.manage') }}</router-link>
    </div>
    <ul class="mt-1 space-y-1">
      <li v-for="rule in rules" :key="rule.group_id" class="flex flex-wrap gap-x-3 text-gray-600 dark:text-dark-300" :data-testid="'share-account-rule-' + rule.group_id">
        <span class="font-medium text-gray-800 dark:text-gray-100">{{ rule.group_name || `#${rule.group_id}` }}</span>
        <span>{{ labels.scheduler(rule.scheduler) }}<template v-if="!rule.effective"> · {{ t('admin.shareScheduling.accountRules.notEffective') }}</template></span>
        <span>{{ t('admin.shareScheduling.accountRules.tier', { tier: rule.tier }) }}</span>
        <span>{{ t('admin.shareScheduling.accountRules.weight', { weight: rule.weight }) }}</span>
        <span>{{ ownDomain(rule) ? t('admin.shareScheduling.accountRules.ownDomain') : t('admin.shareScheduling.accountRules.domain', { domain: rule.cache_domain }) }}</span>
        <span v-if="rule.drain" class="text-amber-600 dark:text-amber-400">{{ t('admin.shareScheduling.accountRules.drain') }}</span>
        <span v-if="!rule.configured" class="text-gray-400 dark:text-dark-400">{{ t('admin.shareScheduling.accountRules.defaults') }}</span>
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
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'

/**
 * Read-only share scheduling rules of an account in its groups. It renders
 * nothing when no group stores a policy or the rules cannot be loaded, so
 * the account editor never depends on it.
 */
const props = defineProps<{ accountId: number }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const rules = ref<ShareAccountGroupRule[]>([])
let request = 0

function ownDomain(rule: ShareAccountGroupRule): boolean {
  return rule.cache_domain === `account:${props.accountId}`
}

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
