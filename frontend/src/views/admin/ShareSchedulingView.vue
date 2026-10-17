<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-5 pb-8">
      <div v-if="store.settings && !store.settings.enabled" class="flex flex-wrap items-center justify-between gap-3 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="share-global-off">
        <span>{{ t('admin.shareScheduling.globalOff') }}</span>
        <RouterLink to="/admin/settings?tab=gateway" class="btn btn-secondary btn-sm">{{ t('admin.shareScheduling.openSettings') }}</RouterLink>
      </div>
      <p v-if="store.settingsFailed" class="rounded-xl bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert">{{ t('admin.shareScheduling.loadSettingsFailed') }}</p>

      <p v-if="store.groupsState === 'unavailable'" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ t('admin.shareScheduling.policy.unavailable') }}</p>

      <div class="grid min-w-0 gap-5 lg:grid-cols-[280px_minmax(0,1fr)]">
        <ShareGroupList
          :groups="store.groups"
          :selected="store.selected"
          :drafts="store.draftGroups"
          :loading="store.groupsState === 'loading'"
          :failed="store.groupsState === 'failed' || store.groupsState === 'unavailable'"
          @select="selectGroup"
          @retry="store.loadGroups()"
        />

        <main class="min-w-0 space-y-5">
          <div v-if="store.selected === null" class="card p-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.groups.selectHint') }}</div>
          <template v-else>
            <div class="card flex flex-wrap items-center justify-between gap-3 p-4">
              <div class="min-w-0">
                <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white" data-testid="share-group-title">{{ selectedName }}</h2>
                <p v-if="store.current" class="text-xs text-gray-500 dark:text-dark-400">{{ store.current.view.platform }} · {{ labels.scheduler(store.current.view.scheduler) }}</p>
              </div>
              <div class="tabs inline-flex" role="tablist" :aria-label="t('admin.shareScheduling.title')">
                <button
                  v-for="item in tabs"
                  :key="item"
                  type="button"
                  role="tab"
                  class="tab"
                  :class="tab === item ? 'tab-active' : ''"
                  :aria-selected="tab === item"
                  :data-testid="'share-tab-' + item"
                  @click="tab = item"
                >{{ t('admin.shareScheduling.tabs.' + item) }}</button>
              </div>
            </div>

            <div v-if="store.policyState === 'loading' && !store.current" class="card space-y-3 p-5" role="status" :aria-label="t('common.loading')">
              <div v-for="row in 4" :key="row" class="h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
            </div>
            <div v-else-if="(store.policyState === 'failed' || store.policyState === 'unavailable') && !store.current" class="card space-y-3 p-5" role="alert">
              <p class="text-sm text-red-700 dark:text-red-300">{{ store.policyState === 'unavailable' ? t('admin.shareScheduling.policy.unavailable') : t('admin.shareScheduling.policy.loadFailed') }}</p>
              <p v-if="store.policyError" class="text-xs text-gray-500 dark:text-dark-400">{{ store.policyError }}</p>
              <button type="button" class="btn btn-secondary btn-sm" @click="store.reload()">{{ t('admin.shareScheduling.policy.reload') }}</button>
            </div>
            <template v-else-if="store.current">
              <SharePolicyEditor v-show="tab === 'policy'" @reload="reloadPolicy" />
              <ShareReportPanel v-if="tab === 'report'" :group-id="store.selected" :sample-rate="store.settings?.attribution_sample_rate" :decision-samples="store.settings?.decision_samples" />
              <ShareExplainPanel v-if="tab === 'explain'" :group-id="store.selected" :members="store.current.view.members" />
            </template>
          </template>
        </main>
      </div>
    </div>

    <BaseDialog :show="confirm !== null" :title="t('admin.shareScheduling.policy.unsavedTitle')" width="narrow" @close="answer(false)">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ confirm === 'reload' ? t('admin.shareScheduling.policy.unsavedReload') : t('admin.shareScheduling.policy.unsavedLeave') }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" data-testid="share-stay" @click="answer(false)">{{ t('admin.shareScheduling.policy.stay') }}</button>
          <button type="button" class="btn btn-danger" data-testid="share-leave" @click="answer(true)">{{ confirm === 'reload' ? t('admin.shareScheduling.policy.discardReload') : t('admin.shareScheduling.policy.leave') }}</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ShareGroupList from '@/components/admin/shareScheduling/ShareGroupList.vue'
import SharePolicyEditor from '@/components/admin/shareScheduling/SharePolicyEditor.vue'
import ShareReportPanel from '@/components/admin/shareScheduling/ShareReportPanel.vue'
import ShareExplainPanel from '@/components/admin/shareScheduling/ShareExplainPanel.vue'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { useShareSchedulingStore } from '@/stores/shareScheduling'

type ShareTab = 'policy' | 'report' | 'explain'

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const route = useRoute()
const router = useRouter()
const store = useShareSchedulingStore()
const tabs: ShareTab[] = ['policy', 'report', 'explain']
const tab = ref<ShareTab>('policy')
const confirm = ref<'leave' | 'reload' | null>(null)
let resolveConfirm: ((ok: boolean) => void) | null = null

const selectedName = computed(() => store.current?.view.group_name ?? store.groups.find(g => g.group_id === store.selected)?.name ?? '')

function ask(kind: 'leave' | 'reload'): Promise<boolean> {
  resolveConfirm?.(false)
  confirm.value = kind
  return new Promise(resolve => { resolveConfirm = resolve })
}

function answer(ok: boolean) {
  confirm.value = null
  const resolve = resolveConfirm
  resolveConfirm = null
  resolve?.(ok)
}

async function selectGroup(groupId: number) {
  if (groupId === store.selected) return
  await store.select(groupId)
  if (String(route.query.group ?? '') !== String(groupId)) {
    void router.replace({ query: { ...route.query, group: String(groupId) } })
  }
}

async function reloadPolicy() {
  if (store.dirty && !(await ask('reload'))) return
  await store.reload()
}

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!store.anyDirty) return
  event.preventDefault()
  event.returnValue = ''
}

onBeforeRouteLeave(async to => {
  if (to.path === route.path || !store.anyDirty) return true
  return ask('leave')
})

onMounted(async () => {
  window.addEventListener('beforeunload', onBeforeUnload)
  void store.loadSettings()
  await store.loadGroups()
  const requested = Number(route.query.group)
  if (Number.isSafeInteger(requested) && requested > 0) await store.select(requested)
})

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  answer(false)
  store.clear()
})
</script>
