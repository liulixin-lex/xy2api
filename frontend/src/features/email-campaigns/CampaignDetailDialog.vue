<template>
  <BaseDialog :show="show" :title="dialogTitle" width="extra-wide" @close="emit('close')">
    <div v-if="!detail" class="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ loadError || t('common.loading') }}
    </div>

    <div v-else class="space-y-5">
      <div class="flex flex-wrap items-center gap-2">
        <span :class="['badge', statusBadgeClass(detail.status)]">
          {{ t(`admin.emailCampaigns.status.${detail.status}`) }}
        </span>
        <span class="text-sm text-gray-500 dark:text-dark-400">#{{ detail.id }}</span>
      </div>

      <div
        :class="[
          'rounded-xl px-4 py-3 text-sm',
          detail.status === 'paused' && detail.pause_reason !== 'manual'
            ? 'border border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200'
            : 'bg-gray-50 text-gray-800 dark:bg-dark-800/60 dark:text-gray-200',
        ]"
        data-testid="email-campaign-status-sentence"
      >
        {{ statusSentence }}
        <div class="progress mt-2">
          <div class="progress-bar" :style="{ width: `${progressPercent(detail)}%` }"></div>
        </div>
      </div>

      <div class="card p-4">
        <h4 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.emailCampaigns.detail.audienceTitle') }}
        </h4>
        <dl class="grid grid-cols-1 gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[auto_1fr]">
          <template v-for="item in audienceRecap" :key="item.label">
            <dt class="text-gray-500 dark:text-dark-400">{{ item.label }}</dt>
            <dd class="text-gray-800 dark:text-gray-200">{{ item.value }}</dd>
          </template>
        </dl>
      </div>

      <div class="space-y-3">
        <div class="flex flex-wrap items-center gap-3">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.emailCampaigns.detail.recipientsTitle') }}
          </h4>
          <Select
            v-model="recipientFilter.status"
            :options="statusOptions"
            class="w-36"
            @change="reloadRecipientsFromStart"
          />
          <div class="min-w-0 flex-1 sm:max-w-64">
            <input
              v-model="recipientFilter.search"
              type="text"
              class="input"
              :placeholder="t('admin.emailCampaigns.detail.searchEmail')"
              @input="handleSearch"
            />
          </div>
        </div>

        <DataTable :columns="recipientColumns" :data="recipients" :loading="recipientsLoading" row-key="user_id">
          <template #cell-status="{ value }">
            <span :class="['badge', recipientBadgeClass(value)]">
              {{ t(`admin.emailCampaigns.recipientStatus.${value}`) }}
            </span>
          </template>
          <template #cell-last_error="{ row }">
            <div class="max-w-md text-sm">
              <span v-if="row.uncertain" class="badge badge-warning mr-1.5">
                {{ t('admin.emailCampaigns.detail.maybeDelivered') }}
              </span>
              <span class="break-words text-gray-600 dark:text-gray-300">{{ row.last_error || '-' }}</span>
            </div>
          </template>
          <template #cell-sent_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ value ? formatDateTime(value) : '-' }}</span>
          </template>
        </DataTable>

        <Pagination
          v-if="recipientPagination.total > 0"
          :page="recipientPagination.page"
          :total="recipientPagination.total"
          :page-size="recipientPagination.page_size"
          @update:page="handleRecipientPage"
          @update:pageSize="handleRecipientPageSize"
        />
      </div>

      <details class="rounded-xl border border-gray-200 dark:border-dark-700">
        <summary class="cursor-pointer select-none px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.emailCampaigns.detail.content') }}
        </summary>
        <div class="bg-gray-100 p-3 dark:bg-dark-900">
          <iframe
            class="h-[32rem] w-full rounded-md border border-gray-200 bg-white dark:border-dark-700"
            sandbox=""
            :srcdoc="detail.preview_html"
            :title="t('admin.emailCampaigns.detail.content')"
          ></iframe>
        </div>
      </details>
    </div>

    <template #footer>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <CampaignActionButtons
          v-if="detail"
          :campaign="detail"
          :busy="actions.busyId.value === detail.id || retrying"
          @pause="actions.pause"
          @resume="actions.resume"
          @cancel="actions.requestCancel"
        >
          <button
            v-if="canRetry"
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="retrying"
            data-testid="email-campaign-retry"
            @click="showRetryConfirm = true"
          >
            {{ t('admin.emailCampaigns.detail.retryFailed', { n: formatCount(detail.counts.failed) }) }}
          </button>
        </CampaignActionButtons>
        <span v-else></span>
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
      </div>
    </template>
  </BaseDialog>

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

  <ConfirmDialog
    :show="showRetryConfirm"
    :title="t('admin.emailCampaigns.detail.retryTitle')"
    :message="retryMessage"
    :confirm-text="t('admin.emailCampaigns.detail.retryConfirm')"
    @confirm="confirmRetry"
    @cancel="showRetryConfirm = false"
  >
    <p
      v-if="detail && detail.counts.uncertain > 0"
      class="rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
    >
      {{ t('admin.emailCampaigns.detail.retryUncertain', { n: formatCount(detail.counts.uncertain) }) }}
    </p>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'
import type { Column } from '@/components/common/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import * as api from './api'
import {
  campaignErrorMessage,
  estimateMinutes,
  formatCount,
  formatDuration,
  isAbortError,
  progressPercent,
  statusBadgeClass,
} from './campaignFormat'
import { useCampaignActions } from './useCampaignActions'
import CampaignActionButtons from './CampaignActionButtons.vue'
import type { Campaign, CampaignDetail, CampaignSettings, Recipient, RecipientStatus } from './types'

const POLL_INTERVAL_MS = 5000

const props = defineProps<{
  show: boolean
  campaignId: number | null
  settings: CampaignSettings | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'changed'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const detail = ref<CampaignDetail | null>(null)
const loadError = ref('')

const dialogTitle = computed(() => detail.value?.subject || t('admin.emailCampaigns.detail.title'))

async function loadDetail() {
  const id = props.campaignId
  if (id === null) return
  try {
    const res = await api.getCampaign(id)
    if (props.campaignId !== id) return
    detail.value = res
    loadError.value = ''
  } catch (err) {
    if (props.campaignId !== id) return
    loadError.value = campaignErrorMessage(err, t)
  }
}

function mergeCampaign(c: Campaign) {
  if (detail.value && detail.value.id === c.id) detail.value = { ...detail.value, ...c }
}

// ===== Status sentence =====
const statusSentence = computed(() => {
  const d = detail.value
  if (!d) return ''
  const c = d.counts
  const base = {
    sent: formatCount(c.sent),
    total: formatCount(c.total),
    failed: formatCount(c.failed),
    skipped: formatCount(c.skipped),
  }
  switch (d.status) {
    case 'sending': {
      if (d.waiting_for_limit === 'day') return t('admin.emailCampaigns.detail.sentence.waitingDay', base)
      if (d.waiting_for_limit === 'minute') return t('admin.emailCampaigns.detail.sentence.waitingMinute', base)
      const remaining = c.pending + c.sending
      const s = props.settings
      if (!s || remaining <= 0) return t('admin.emailCampaigns.detail.sentence.sending', base)
      return t('admin.emailCampaigns.detail.sentence.sendingEta', {
        ...base,
        eta: formatDuration(estimateMinutes(remaining, s.per_minute, s.per_day), t),
      })
    }
    case 'paused':
      if (d.pause_reason === 'failures') {
        return t('admin.emailCampaigns.detail.sentence.pausedFailures', {
          ...base,
          n: d.consecutive_failures || 5,
          error: d.last_error || '-',
        })
      }
      if (d.pause_reason === 'auth') {
        return t('admin.emailCampaigns.detail.sentence.pausedAuth', { ...base, error: d.last_error || '-' })
      }
      return t('admin.emailCampaigns.detail.sentence.paused', base)
    case 'completed':
      return t('admin.emailCampaigns.detail.sentence.completed', base)
    case 'canceled':
      return t('admin.emailCampaigns.detail.sentence.canceled', base)
    default:
      return ''
  }
})

// ===== Audience recap =====
const audienceRecap = computed(() => {
  const d = detail.value
  if (!d) return []
  const a = d.audience
  let registration = t('admin.emailCampaigns.compose.anyRegistration')
  if (a.registered_from && a.registered_to) {
    registration = t('admin.emailCampaigns.compose.registrationRange', { from: a.registered_from, to: a.registered_to })
  } else if (a.registered_from) {
    registration = t('admin.emailCampaigns.compose.registrationFrom', { from: a.registered_from })
  } else if (a.registered_to) {
    registration = t('admin.emailCampaigns.compose.registrationTo', { to: a.registered_to })
  }
  if (a.registered_from || a.registered_to) registration += ` (${a.timezone})`
  const activity = a.inactive_days
    ? t('admin.emailCampaigns.detail.recap.dormant', { n: a.inactive_days })
    : t('admin.emailCampaigns.compose.anyActivity')
  const skipped = a.skipped
  return [
    { label: t('admin.emailCampaigns.detail.recap.registration'), value: registration },
    { label: t('admin.emailCampaigns.detail.recap.activity'), value: activity },
    {
      label: t('admin.emailCampaigns.detail.recap.excluded'),
      value: t('admin.emailCampaigns.detail.recap.excludedCount', {
        n: formatCount(a.exclude_user_ids?.length ?? 0),
      }),
    },
    {
      label: t('admin.emailCampaigns.detail.recap.skippedAtStart'),
      value: t('admin.emailCampaigns.detail.recap.skippedCounts', {
        noEmail: formatCount(skipped?.no_valid_email),
        suppressed: formatCount(skipped?.suppressed),
        excluded: formatCount(skipped?.excluded),
        duplicate: formatCount(skipped?.duplicate),
      }),
    },
    {
      label: t('admin.emailCampaigns.detail.recap.createdBy'),
      value: `${d.created_by?.email || '-'} · ${formatDateTime(d.created_at)}`,
    },
  ]
})

// ===== Recipients =====
const recipients = ref<Recipient[]>([])
const recipientsLoading = ref(false)
const recipientFilter = reactive({ status: '' as '' | RecipientStatus, search: '' })
const recipientPagination = reactive({
  page: 1,
  page_size: Math.min(getPersistedPageSize(), api.MAX_PAGE_SIZE),
  total: 0,
})

const statusOptions = computed(() => [
  { value: '', label: t('admin.emailCampaigns.detail.allStatus') },
  { value: 'pending', label: t('admin.emailCampaigns.recipientStatus.pending') },
  { value: 'sent', label: t('admin.emailCampaigns.recipientStatus.sent') },
  { value: 'failed', label: t('admin.emailCampaigns.recipientStatus.failed') },
  { value: 'skipped', label: t('admin.emailCampaigns.recipientStatus.skipped') },
])

const recipientColumns = computed<Column[]>(() => [
  { key: 'email', label: t('admin.emailCampaigns.detail.columns.email') },
  { key: 'status', label: t('admin.emailCampaigns.detail.columns.status') },
  { key: 'last_error', label: t('admin.emailCampaigns.detail.columns.error') },
  { key: 'sent_at', label: t('admin.emailCampaigns.detail.columns.sentAt') },
])

function recipientBadgeClass(status: RecipientStatus): string {
  switch (status) {
    case 'sent':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    case 'sending':
      return 'badge-primary'
    default:
      return 'badge-gray'
  }
}

let recipientController: AbortController | null = null

async function loadRecipients(silent = false) {
  const id = props.campaignId
  if (id === null) return
  recipientController?.abort()
  const controller = new AbortController()
  recipientController = controller
  if (!silent) recipientsLoading.value = true
  try {
    const res = await api.listRecipients(
      id,
      {
        status: recipientFilter.status || undefined,
        search: recipientFilter.search.trim() || undefined,
        page: recipientPagination.page,
        page_size: recipientPagination.page_size,
      },
      { signal: controller.signal },
    )
    if (recipientController !== controller) return
    recipients.value = res.items ?? []
    recipientPagination.total = res.total
  } catch (err) {
    if (recipientController !== controller || isAbortError(err)) return
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    if (recipientController === controller) {
      recipientsLoading.value = false
      recipientController = null
    }
  }
}

function reloadRecipientsFromStart() {
  recipientPagination.page = 1
  void loadRecipients()
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
function handleSearch() {
  if (searchTimer !== null) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchTimer = null
    reloadRecipientsFromStart()
  }, 300)
}

function handleRecipientPage(page: number) {
  recipientPagination.page = page
  void loadRecipients()
}

function handleRecipientPageSize(size: number) {
  recipientPagination.page_size = Math.min(size, api.MAX_PAGE_SIZE)
  reloadRecipientsFromStart()
}

// ===== Actions =====
const actions = useCampaignActions((updated) => {
  mergeCampaign(updated)
  emit('changed')
  void loadRecipients(true)
})

const cancelMessage = computed(() => {
  const target = actions.cancelTarget.value
  if (!target) return ''
  return t('admin.emailCampaigns.actions.cancelMessage', {
    subject: target.subject,
    n: formatCount(target.counts.pending + target.counts.sending),
  })
})

const canRetry = computed(() => {
  const d = detail.value
  return !!d && d.counts.failed > 0 && (d.status === 'paused' || d.status === 'completed')
})

const showRetryConfirm = ref(false)
const retrying = ref(false)

const retryMessage = computed(() =>
  t('admin.emailCampaigns.detail.retryMessage', { n: formatCount(detail.value?.counts.failed ?? 0) }),
)

async function confirmRetry() {
  showRetryConfirm.value = false
  const d = detail.value
  if (!d || retrying.value) return
  retrying.value = true
  try {
    const res = await api.retryFailed(d.id)
    mergeCampaign(res.campaign)
    appStore.showSuccess(t('admin.emailCampaigns.detail.retryDone', { n: formatCount(res.requeued) }))
    emit('changed')
    void loadRecipients(true)
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    retrying.value = false
  }
}

// ===== Open / poll =====
let pollTimer: ReturnType<typeof setInterval> | null = null

function stopPolling() {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

watch(
  () => [props.show, props.campaignId] as const,
  ([show, id]) => {
    stopPolling()
    if (!show || id === null) return
    detail.value = null
    loadError.value = ''
    recipients.value = []
    recipientFilter.status = ''
    recipientFilter.search = ''
    recipientPagination.page = 1
    recipientPagination.total = 0
    void loadDetail()
    void loadRecipients()
    pollTimer = setInterval(() => {
      if (detail.value?.status !== 'sending') return
      void loadDetail()
      void loadRecipients(true)
    }, POLL_INTERVAL_MS)
  },
  { immediate: true },
)

onUnmounted(() => {
  stopPolling()
  if (searchTimer !== null) clearTimeout(searchTimer)
  recipientController?.abort()
})
</script>
