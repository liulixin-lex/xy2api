<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-5 pb-8">
      <div class="card flex flex-wrap items-center justify-between gap-3 px-5 py-3" data-testid="share-header">
        <div class="min-w-0">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.global.title') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.global.hint') }}</p>
        </div>
        <ShareGlobalSwitch
          v-if="store.settings"
          :model-value="store.settings.enabled"
          :labelled="false"
          :disabled="!canManage"
          @update:model-value="store.setGlobalEnabled"
        />
        <div v-else-if="store.settingsFailed" class="flex items-center gap-2 text-sm text-red-700 dark:text-red-300" role="alert">
          {{ t('admin.shareScheduling.global.loadFailed') }}
          <button type="button" class="btn btn-secondary btn-sm" @click="store.loadSettings()">{{ t('admin.shareScheduling.global.reload') }}</button>
        </div>
      </div>

      <p v-if="store.groupsState === 'unavailable'" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ t('admin.shareScheduling.mode.unavailable') }}</p>

      <div class="grid min-w-0 gap-5 lg:grid-cols-[280px_minmax(0,1fr)]">
        <ShareGroupList
          :groups="store.groups"
          :selected="store.selected"
          :loading="store.groupsState === 'loading'"
          :failed="store.groupsState === 'failed' || store.groupsState === 'unavailable'"
          @select="selectGroup"
          @retry="store.loadGroups()"
        />

        <main class="min-w-0 space-y-5">
          <div v-if="store.selected === null" class="card p-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.groups.selectHint') }}</div>
          <template v-else>
            <div v-if="store.policyState === 'loading' && !store.view" class="card space-y-3 p-5" role="status" :aria-label="t('common.loading')">
              <div v-for="row in 4" :key="row" class="h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
            </div>
            <div v-else-if="(store.policyState === 'failed' || store.policyState === 'unavailable') && !store.view" class="card space-y-3 p-5" role="alert">
              <p class="text-sm text-red-700 dark:text-red-300">{{ store.policyState === 'unavailable' ? t('admin.shareScheduling.mode.unavailable') : t('admin.shareScheduling.mode.loadFailed') }}</p>
              <p v-if="store.policyError" class="text-xs text-gray-500 dark:text-dark-400">{{ store.policyError }}</p>
              <button type="button" class="btn btn-secondary btn-sm" @click="store.reload()">{{ t('admin.shareScheduling.mode.reload') }}</button>
            </div>
            <template v-else-if="store.view">
              <ShareModeCard :view="store.view" />
              <ShareHealthCard />
              <ShareAttentionList :view="store.view" :global-enabled="store.settings?.enabled ?? null" />
              <ShareTrafficTable :view="store.view" :group-names="store.groupNames" @changed="store.reload()" />
              <details class="card p-5" data-testid="share-details">
                <summary class="cursor-pointer font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.details.title') }}</summary>
                <ShareReportDetails class="mt-4" :report="store.report" />
              </details>
              <details class="card p-5" data-testid="share-troubleshoot">
                <summary class="cursor-pointer font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.explain.title') }}</summary>
                <ShareExplainPanel class="mt-4" :group-id="store.view.group_id" :members="store.view.members" />
              </details>
            </template>
          </template>
        </main>
      </div>
    </div>

    <ShareChangeDialog />
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ShareAttentionList from '@/components/admin/shareScheduling/ShareAttentionList.vue'
import ShareChangeDialog from '@/components/admin/shareScheduling/ShareChangeDialog.vue'
import ShareExplainPanel from '@/components/admin/shareScheduling/ShareExplainPanel.vue'
import ShareGlobalSwitch from '@/components/admin/shareScheduling/ShareGlobalSwitch.vue'
import ShareGroupList from '@/components/admin/shareScheduling/ShareGroupList.vue'
import ShareHealthCard from '@/components/admin/shareScheduling/ShareHealthCard.vue'
import ShareModeCard from '@/components/admin/shareScheduling/ShareModeCard.vue'
import ShareReportDetails from '@/components/admin/shareScheduling/ShareReportDetails.vue'
import ShareTrafficTable from '@/components/admin/shareScheduling/ShareTrafficTable.vue'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { useAdminPermission } from '@/composables/useAdminPermission'

const { t } = useI18n()
// 全局开关、调度模式和会话保持的保存需要份额调度的管理权限
const { canManage } = useAdminPermission('share_scheduling')
const route = useRoute()
const router = useRouter()
const store = useShareSchedulingStore()

async function selectGroup(groupId: number) {
  if (groupId === store.selected) return
  if (String(route.query.group ?? '') !== String(groupId)) {
    void router.replace({ query: { ...route.query, group: String(groupId) } })
  }
  await store.select(groupId)
}

/** The group from the link, else the first group running share scheduling, else the first group. */
function initialGroup(): number | null {
  const requested = Number(route.query.group)
  if (Number.isSafeInteger(requested) && requested > 0) return requested
  return store.activeGroups[0]?.group_id ?? store.groups[0]?.group_id ?? null
}

onMounted(async () => {
  void store.loadSettings()
  await store.loadGroups()
  const groupId = initialGroup()
  if (groupId !== null) await store.select(groupId)
})

onBeforeUnmount(() => {
  store.clear()
})
</script>
