<template>
  <section class="card p-5" data-testid="share-traffic">
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.traffic.title') }}</h3>
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ view.scheduler === 'share' ? t('admin.shareScheduling.traffic.hintShare') : t('admin.shareScheduling.traffic.hintSub2api') }}</p>
    </div>

    <div v-if="!view.members.length" class="space-y-3 py-6 text-center text-sm text-gray-600 dark:text-dark-300">
      <p>{{ t('admin.shareScheduling.traffic.empty') }}</p>
      <RouterLink to="/admin/accounts" class="btn btn-secondary btn-sm">{{ t('admin.shareScheduling.traffic.manageAccounts') }}</RouterLink>
    </div>
    <div v-else class="mt-3 overflow-x-auto">
      <table class="w-full min-w-[760px] text-left text-sm">
        <thead>
          <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
            <th class="p-2">{{ t('admin.shareScheduling.traffic.columns.account') }}</th>
            <th class="p-2">{{ t('admin.shareScheduling.traffic.columns.status') }}</th>
            <th class="p-2">{{ t('admin.shareScheduling.traffic.columns.loadFactor') }}</th>
            <th class="w-[28%] p-2">{{ t('admin.shareScheduling.traffic.columns.expected') }}</th>
            <th class="whitespace-nowrap p-2 text-right"><span class="cursor-help underline decoration-dotted underline-offset-2" :title="t('admin.shareScheduling.traffic.actualHint')" data-testid="share-traffic-actual-header">{{ t('admin.shareScheduling.traffic.columns.actual') }}</span></th>
            <th class="whitespace-nowrap p-2 text-right"><span class="cursor-help underline decoration-dotted underline-offset-2" :title="t('admin.shareScheduling.traffic.failureHint')" data-testid="share-traffic-failure-header">{{ t('admin.shareScheduling.traffic.columns.failure') }}</span></th>
          </tr>
        </thead>
        <tbody v-for="section in sections" :key="section.priority" :data-testid="'share-priority-' + section.priority">
          <tr class="bg-gray-50/80 dark:bg-dark-700/40">
            <td colspan="6" class="px-2 py-1.5 text-xs">
              <span class="font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.traffic.priority', { priority: section.priority }) }}</span>
              <span class="ml-2" :class="section.active ? 'text-emerald-700 dark:text-emerald-300' : 'text-gray-500 dark:text-dark-400'">
                {{ section.active ? t('admin.shareScheduling.traffic.active') : t('admin.shareScheduling.traffic.standby') }}
              </span>
            </td>
          </tr>
          <tr v-for="member in section.members" :key="member.account_id" class="border-b border-gray-100 align-middle last:border-0 dark:border-dark-700" :data-testid="'share-member-' + member.account_id">
            <td class="max-w-[16rem] p-2">
              <div class="truncate font-medium text-gray-900 dark:text-white" :title="member.name">{{ member.name }}</div>
              <div class="truncate text-xs text-gray-500 dark:text-dark-400">#{{ member.account_id }} · {{ member.platform }}/{{ member.type }}</div>
            </td>
            <td class="p-2 text-xs">
              <span v-if="member.healthy" class="text-emerald-700 dark:text-emerald-300">{{ t('admin.shareScheduling.traffic.healthy') }}</span>
              <template v-else>
                <span class="block text-amber-700 dark:text-amber-300">{{ labels.reason(member.reason) }}</span>
                <span v-if="member.recover_at" class="block text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.traffic.recoverAt', { time: formatDateTimeToMinute(member.recover_at) }) }}</span>
              </template>
            </td>
            <td class="p-2">
              <ShareLoadFactorCell :member="member" :other-groups="otherGroups(member)" @updated="value => onUpdated(member, value)" @error="message => appStore.showError(message)" />
            </td>
            <td class="p-2">
              <ShareShareBar :value="member.expected_share" />
              <span v-if="!member.healthy && member.share_if_healthy > 0" class="mt-0.5 block text-right text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.traffic.ifHealthy', { value: formatSharePercent(member.share_if_healthy) }) }}</span>
              <ShareFactorMarks v-if="member.healthy" :member="member" :standby="!section.active" />
            </td>
            <td class="p-2 text-right tabular-nums text-gray-700 dark:text-dark-200" :data-testid="'share-actual-' + member.account_id">{{ member.actual_share_1h === null ? '—' : formatSharePercent(member.actual_share_1h) }}</td>
            <td class="p-2 text-right tabular-nums" :class="downweighted(member) ? 'font-medium text-red-700 dark:text-red-300' : member.health?.insufficient ? 'text-gray-400 dark:text-dark-500' : 'text-gray-700 dark:text-dark-200'" :data-testid="'share-failure-' + member.account_id">{{ member.health?.rate === null || member.health?.rate === undefined ? '—' : formatSharePercent(member.health.rate) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import ShareFactorMarks from './ShareFactorMarks.vue'
import ShareLoadFactorCell from './ShareLoadFactorCell.vue'
import ShareShareBar from './ShareShareBar.vue'
import type { ShareMember, SharePolicyView } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { useAppStore } from '@/stores/app'
import { formatDateTimeToMinute } from '@/utils/format'
import { formatSharePercent, sharePrioritySections } from '@/utils/shareScheduling'

const props = defineProps<{
  view: SharePolicyView
  groupNames: Map<number, string>
}>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const appStore = useAppStore()

const sections = computed(() => sharePrioritySections(props.view.members, props.view.active_priority))

function otherGroups(member: ShareMember): string[] {
  return member.other_group_ids.map(id => props.groupNames.get(id) ?? `#${id}`)
}

/** The account takes fewer new sessions because it fails more than its peers. */
function downweighted(member: ShareMember): boolean {
  return member.health_factor !== null && member.health_factor < 1
}

function onUpdated(member: ShareMember, value: number | null) {
  appStore.showSuccess(value === null
    ? t('admin.shareScheduling.loadFactor.cleared', { name: member.name })
    : t('admin.shareScheduling.loadFactor.saved', { name: member.name, value }))
  emit('changed')
}
</script>
