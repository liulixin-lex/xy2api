<template>
  <div class="card p-5" data-testid="ticket-user">
    <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('supportTickets.admin.userInfo') }}</h3>
    <p v-if="user?.deleted" class="mt-2 rounded-lg bg-gray-100 px-2 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">
      {{ t('supportTickets.admin.deletedUser') }}
    </p>
    <dl class="mt-3 space-y-2 text-sm">
      <div class="flex justify-between gap-3">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.userId') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">{{ userId }}</dd>
      </div>
      <div class="flex justify-between gap-3">
        <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.email') }}</dt>
        <dd class="flex min-w-0 items-center gap-1">
          <span class="truncate text-gray-900 dark:text-gray-100" :title="user?.email">{{ user?.email || '—' }}</span>
          <button
            v-if="user?.email"
            type="button"
            class="shrink-0 text-gray-400 hover:text-gray-700 dark:hover:text-gray-200"
            :title="t('supportTickets.admin.copyEmail')"
            :aria-label="t('supportTickets.admin.copyEmail')"
            @click="copyEmail"
          >
            <Icon name="copy" size="sm" />
          </button>
        </dd>
      </div>
      <div class="flex justify-between gap-3">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.username') }}</dt>
        <dd class="truncate text-gray-900 dark:text-gray-100">{{ user?.username || '—' }}</dd>
      </div>
      <div v-if="user?.balance !== undefined" class="flex justify-between gap-3">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.balance') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">${{ (user?.balance ?? 0).toFixed(2) }}</dd>
      </div>
      <div class="flex justify-between gap-3">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.accountStatus') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">{{ accountStatus }}</dd>
      </div>
      <div v-if="user?.created_at" class="flex justify-between gap-3">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('supportTickets.admin.registeredAt') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">{{ user?.created_at ? formatDateTimeToMinute(user.created_at) : '—' }}</dd>
      </div>
    </dl>
    <router-link
      v-if="usage.canView.value"
      :to="`/admin/usage?user_id=${userId}`"
      class="mt-3 inline-flex items-center gap-1 text-sm text-primary-600 hover:underline dark:text-primary-400"
      data-testid="support-user-usage"
    >
      {{ t('supportTickets.admin.viewUsage') }}
      <span aria-hidden="true">→</span>
    </router-link>
    <slot />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { SupportTicketUser } from '@/api/supportTickets'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { useAppStore } from '@/stores/app'
import { formatDateTimeToMinute } from '@/utils/format'

// Shared by the ticket detail page and the chat inbox; balance and registration date are absent for operators.
const props = defineProps<{ user: SupportTicketUser | null | undefined; userId: number }>()
const { t } = useI18n()
const appStore = useAppStore()
const usage = useAdminPermission('usage')

const accountStatus = computed(() => {
  const status = props.user?.status
  if (!status) return '—'
  return status === 'active' || status === 'disabled' ? t(`supportTickets.admin.accountStatuses.${status}`) : status
})

async function copyEmail() {
  const email = props.user?.email
  if (!email) return
  try {
    await navigator.clipboard.writeText(email)
    appStore.showSuccess(t('supportTickets.admin.emailCopied'))
  } catch {
    appStore.showError(t('supportTickets.actionFailed'))
  }
}
</script>
