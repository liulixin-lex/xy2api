<template>
  <AppLayout>
    <div class="w-full min-w-0 space-y-6">
      <section
        class="flex flex-col gap-4 border-b border-gray-200 pb-5 dark:border-dark-700 sm:flex-row sm:items-end sm:justify-between"
      >
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('admin.emailCampaigns.title') }}
          </h2>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.emailCampaigns.description') }}
          </p>
        </div>
        <div class="flex flex-shrink-0 items-center gap-2">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="loading"
            :title="t('common.refresh')"
            @click="refreshAll()"
          >
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            <span class="sr-only">{{ t('common.refresh') }}</span>
          </button>
          <span class="inline-flex" :title="createBlockedReason || undefined">
            <button
              type="button"
              class="btn btn-primary"
              :disabled="!canCreate"
              data-testid="email-campaign-new"
              @click="showCompose = true"
            >
              <Icon name="plus" size="sm" />
              {{ t('admin.emailCampaigns.newCampaign') }}
            </button>
          </span>
        </div>
      </section>

      <div
        v-if="setupProblems.length > 0"
        role="alert"
        class="space-y-1 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        <p v-for="problem in setupProblems" :key="problem">{{ problem }}</p>
      </div>

      <div class="card p-4">
        <form class="flex flex-wrap items-center gap-x-5 gap-y-3" @submit.prevent="saveLimits">
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            {{ t('admin.emailCampaigns.limits.perMinute') }}
            <input
              v-model.number="limitsForm.per_minute"
              type="number"
              min="1"
              max="120"
              step="1"
              class="input w-24"
              data-testid="email-campaign-per-minute"
            />
            {{ t('admin.emailCampaigns.limits.unit') }}
          </label>
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            {{ t('admin.emailCampaigns.limits.perDay') }}
            <input
              v-model.number="limitsForm.per_day"
              type="number"
              min="1"
              max="100000"
              step="1"
              class="input w-28"
              data-testid="email-campaign-per-day"
            />
            {{ t('admin.emailCampaigns.limits.unit') }}
          </label>
          <button
            type="submit"
            class="btn btn-secondary"
            :disabled="savingLimits || !settings || !limitsValid || !limitsDirty"
          >
            {{ savingLimits ? t('common.saving') : t('common.save') }}
          </button>
        </form>
        <p v-if="settings && !limitsValid" class="mt-2 text-xs text-red-600 dark:text-red-400">
          {{ t('admin.emailCampaigns.limits.invalid') }}
        </p>
        <p class="input-hint">{{ t('admin.emailCampaigns.limits.hint') }}</p>
        <p v-if="settings" class="input-hint">
          {{
            t('admin.emailCampaigns.limits.sentRecently', {
              minute: formatCount(settings.sent_last_minute),
              day: formatCount(settings.sent_last_24h),
            })
          }}
        </p>
      </div>

      <div v-roving-tabs class="tabs inline-flex" role="tablist" :aria-label="t('admin.emailCampaigns.tabs.aria')">
        <button
          v-for="tab in tabs"
          :key="tab.value"
          type="button"
          role="tab"
          class="tab flex-1 whitespace-nowrap sm:flex-none"
          :class="activeTab === tab.value ? 'tab-active' : ''"
          :aria-selected="activeTab === tab.value"
          @click="activeTab = tab.value"
        >
          {{ tab.label }}
        </button>
      </div>

      <TablePageLayout v-if="activeTab === 'campaigns'">
        <template #table>
          <DataTable :columns="columns" :data="campaigns" :loading="loading" row-key="id">
            <template #cell-subject="{ row }">
              <div class="min-w-0 max-w-xs">
                <div class="truncate font-medium text-gray-900 dark:text-white" :title="row.subject">
                  {{ row.subject }}
                </div>
                <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">#{{ row.id }}</div>
              </div>
            </template>

            <template #cell-status="{ row }">
              <span :class="['badge', statusBadgeClass(row.status)]">
                {{ t(`admin.emailCampaigns.status.${row.status}`) }}
              </span>
            </template>

            <template #cell-progress="{ row }">
              <div class="w-56">
                <div class="text-sm text-gray-700 dark:text-gray-300">
                  {{ progressText(row) }}
                </div>
                <div class="progress mt-1.5">
                  <div class="progress-bar" :style="{ width: `${progressPercent(row)}%` }"></div>
                </div>
              </div>
            </template>

            <template #cell-created_by="{ row }">
              <span class="text-sm text-gray-600 dark:text-gray-300">{{ row.created_by?.email || '-' }}</span>
            </template>

            <template #cell-created_at="{ row }">
              <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(row.created_at) }}</span>
            </template>

            <template #cell-actions="{ row }">
              <CampaignActionButtons
                :campaign="row"
                :busy="actions.busyId.value === row.id"
                @pause="actions.pause"
                @resume="actions.resume"
                @cancel="actions.requestCancel"
              >
                <button type="button" class="btn btn-secondary btn-sm" @click="openDetail(row)">
                  {{ t('admin.emailCampaigns.actions.view') }}
                </button>
              </CampaignActionButtons>
            </template>

            <template #empty>
              <EmptyState
                :title="t('admin.emailCampaigns.empty.title')"
                :description="t('admin.emailCampaigns.empty.description')"
              />
            </template>
          </DataTable>
        </template>

        <template #pagination>
          <Pagination
            v-if="pagination.total > 0"
            :page="pagination.page"
            :total="pagination.total"
            :page-size="pagination.page_size"
            @update:page="handlePageChange"
            @update:pageSize="handlePageSizeChange"
          />
        </template>
      </TablePageLayout>

      <SuppressionList v-else />
    </div>

    <CampaignComposeDialog
      :show="showCompose"
      :settings="settings"
      :can-create="canCreate"
      :default-test-email="authStore.user?.email || ''"
      @close="showCompose = false"
      @created="handleCreated"
    />

    <CampaignDetailDialog
      :show="detailId !== null"
      :campaign-id="detailId"
      :settings="settings"
      @close="detailId = null"
      @changed="refreshAll(true)"
    />

    <ConfirmDialog
      :show="actions.cancelTarget.value !== null"
      :title="t('admin.emailCampaigns.actions.cancelTitle')"
      :message="cancelMessage"
      :confirm-text="t('admin.emailCampaigns.actions.cancelConfirm')"
      :cancel-text="t('admin.emailCampaigns.actions.keepSending')"
      danger
      @confirm="actions.confirmCancel"
      @cancel="actions.cancelTarget.value = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'
import { vRovingTabs } from '@/utils/rovingTabs'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import * as api from './api'
import {
  campaignErrorMessage,
  formatCount,
  isAbortError,
  isActiveStatus,
  progressPercent,
  statusBadgeClass,
} from './campaignFormat'
import { useCampaignActions } from './useCampaignActions'
import CampaignActionButtons from './CampaignActionButtons.vue'
import CampaignComposeDialog from './CampaignComposeDialog.vue'
import CampaignDetailDialog from './CampaignDetailDialog.vue'
import SuppressionList from './SuppressionList.vue'
import type { Campaign, CampaignSettings } from './types'

const POLL_INTERVAL_MS = 5000

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

type TabValue = 'campaigns' | 'suppressions'
const activeTab = ref<TabValue>('campaigns')
const tabs = computed(() => [
  { value: 'campaigns' as const, label: t('admin.emailCampaigns.tabs.campaigns') },
  { value: 'suppressions' as const, label: t('admin.emailCampaigns.tabs.suppressions') },
])

// ===== Settings / send limits =====
const settings = ref<CampaignSettings | null>(null)
const limitsForm = reactive({ per_minute: 20, per_day: 500 })
const savingLimits = ref(false)

const limitsValid = computed(
  () =>
    Number.isInteger(limitsForm.per_minute) &&
    limitsForm.per_minute >= 1 &&
    limitsForm.per_minute <= 120 &&
    Number.isInteger(limitsForm.per_day) &&
    limitsForm.per_day >= 1 &&
    limitsForm.per_day <= 100000,
)
const limitsDirty = computed(
  () =>
    !!settings.value &&
    (limitsForm.per_minute !== settings.value.per_minute || limitsForm.per_day !== settings.value.per_day),
)

function applySettings(next: CampaignSettings, resetForm: boolean) {
  settings.value = next
  if (resetForm) {
    limitsForm.per_minute = next.per_minute
    limitsForm.per_day = next.per_day
  }
}

async function loadSettings(resetForm: boolean) {
  try {
    applySettings(await api.getSettings(), resetForm)
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  }
}

async function saveLimits() {
  if (!limitsValid.value || savingLimits.value) return
  savingLimits.value = true
  try {
    const next = await api.updateSettings({ per_minute: limitsForm.per_minute, per_day: limitsForm.per_day })
    applySettings(next, true)
    appStore.showSuccess(t('admin.emailCampaigns.limits.saved'))
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    savingLimits.value = false
  }
}

const setupProblems = computed(() => {
  const s = settings.value
  if (!s) return []
  const out: string[] = []
  if (!s.smtp_configured) out.push(t('admin.emailCampaigns.setup.smtpMissing'))
  if (!s.site_url_ok) {
    out.push(
      s.site_url
        ? t('admin.emailCampaigns.setup.siteUrlNotHttps', { url: s.site_url })
        : t('admin.emailCampaigns.setup.siteUrlMissing'),
    )
  }
  return out
})

// ===== Campaign list =====
const campaigns = ref<Campaign[]>([])
const loading = ref(false)
const pagination = reactive({
  page: 1,
  page_size: Math.min(getPersistedPageSize(), api.MAX_PAGE_SIZE),
  total: 0,
})

/** The one campaign that is sending or paused, wherever it sits in the list. */
const activeCampaign = ref<Campaign | null>(null)

const columns = computed<Column[]>(() => [
  { key: 'subject', label: t('admin.emailCampaigns.columns.subject') },
  { key: 'status', label: t('admin.emailCampaigns.columns.status') },
  { key: 'progress', label: t('admin.emailCampaigns.columns.progress') },
  { key: 'created_by', label: t('admin.emailCampaigns.columns.createdBy') },
  { key: 'created_at', label: t('admin.emailCampaigns.columns.createdAt') },
  { key: 'actions', label: t('admin.emailCampaigns.columns.actions') },
])

let listController: AbortController | null = null

async function loadCampaigns(silent = false) {
  listController?.abort()
  const controller = new AbortController()
  listController = controller
  if (!silent) loading.value = true
  try {
    const res = await api.listCampaigns(
      { page: pagination.page, page_size: pagination.page_size },
      { signal: controller.signal },
    )
    if (listController !== controller) return
    campaigns.value = res.items ?? []
    pagination.total = res.total
  } catch (err) {
    if (listController !== controller || isAbortError(err)) return
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    if (listController === controller) {
      loading.value = false
      listController = null
    }
  }
}

async function loadActive() {
  try {
    const [sending, paused] = await Promise.all([
      api.listCampaigns({ status: 'sending', page: 1, page_size: 1 }),
      api.listCampaigns({ status: 'paused', page: 1, page_size: 1 }),
    ])
    const found = [...(sending.items ?? []), ...(paused.items ?? [])]
    activeCampaign.value = found.find((c) => isActiveStatus(c.status)) ?? null
  } catch {
    // Not fatal: the list rows and the server's ACTIVE_EXISTS check still guard creation.
    activeCampaign.value = campaigns.value.find((c) => isActiveStatus(c.status)) ?? null
  }
}

async function refreshAll(silent = false) {
  await Promise.all([loadCampaigns(silent), loadActive(), silent ? loadSettings(false) : Promise.resolve()])
}

function handlePageChange(page: number) {
  pagination.page = page
  loadCampaigns()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = Math.min(size, api.MAX_PAGE_SIZE)
  pagination.page = 1
  loadCampaigns()
}

const blockingCampaign = computed(
  () => activeCampaign.value ?? campaigns.value.find((c) => isActiveStatus(c.status)) ?? null,
)
const anySending = computed(
  () => blockingCampaign.value?.status === 'sending' || campaigns.value.some((c) => c.status === 'sending'),
)

const createBlockedReason = computed(() => {
  if (!settings.value) return ''
  if (setupProblems.value.length > 0) return setupProblems.value[0]
  if (blockingCampaign.value) return t('admin.emailCampaigns.setup.activeExists')
  return ''
})
const canCreate = computed(() => !!settings.value && !createBlockedReason.value)

function progressText(c: Campaign): string {
  return t('admin.emailCampaigns.progress', {
    sent: formatCount(c.counts.sent),
    total: formatCount(c.counts.total),
    failed: formatCount(c.counts.failed),
    skipped: formatCount(c.counts.skipped),
  })
}

// ===== Actions =====
const actions = useCampaignActions(() => {
  void refreshAll(true)
})

const cancelMessage = computed(() => {
  const target = actions.cancelTarget.value
  if (!target) return ''
  return t('admin.emailCampaigns.actions.cancelMessage', {
    subject: target.subject,
    n: formatCount(target.counts.pending + target.counts.sending),
  })
})

// ===== Dialogs =====
const showCompose = ref(false)
const detailId = ref<number | null>(null)

function openDetail(row: Campaign) {
  detailId.value = row.id
}

function handleCreated(campaign: Campaign) {
  showCompose.value = false
  activeTab.value = 'campaigns'
  pagination.page = 1
  void refreshAll(true)
  detailId.value = campaign.id
}

// ===== Auto refresh while sending =====
let pollTimer: ReturnType<typeof setInterval> | null = null

function stopPolling() {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

watch(anySending, (sending) => {
  if (sending && pollTimer === null) {
    pollTimer = setInterval(() => {
      if (activeTab.value === 'campaigns') void refreshAll(true)
    }, POLL_INTERVAL_MS)
  } else if (!sending) {
    stopPolling()
  }
})

onMounted(() => {
  void loadSettings(true)
  void refreshAll()
})

onUnmounted(() => {
  stopPolling()
  listController?.abort()
})
</script>
