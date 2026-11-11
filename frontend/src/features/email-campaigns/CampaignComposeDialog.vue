<template>
  <BaseDialog
    :show="show"
    :title="t('admin.emailCampaigns.compose.title')"
    width="full"
    :close-on-escape="false"
    @close="emit('close')"
  >
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <!-- Form -->
      <div class="space-y-5">
        <div>
          <label class="input-label" :for="ids.subject">{{ t('admin.emailCampaigns.compose.subject') }}</label>
          <input
            :id="ids.subject"
            v-model="form.subject"
            type="text"
            maxlength="200"
            class="input"
            data-testid="email-campaign-subject"
          />
        </div>

        <div>
          <div class="mb-1.5 flex flex-wrap items-center justify-between gap-2">
            <label class="input-label mb-0" :for="ids.body">{{ t('admin.emailCampaigns.compose.body') }}</label>
            <div class="flex flex-wrap gap-1.5">
              <button
                v-for="placeholder in PLACEHOLDERS"
                :key="placeholder"
                type="button"
                class="rounded-md bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-700 transition-colors hover:bg-gray-200 dark:bg-dark-700 dark:text-gray-200 dark:hover:bg-dark-600"
                :title="t('admin.emailCampaigns.compose.insertPlaceholder')"
                @click="insertPlaceholder(placeholder)"
              >
                {{ placeholder }}
              </button>
            </div>
          </div>
          <textarea
            :id="ids.body"
            ref="bodyRef"
            v-model="form.body"
            rows="12"
            class="input font-mono text-sm"
            data-testid="email-campaign-body"
          ></textarea>
          <p class="input-hint">{{ t('admin.emailCampaigns.compose.bodyHint') }}</p>
        </div>

        <div class="space-y-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.emailCampaigns.compose.byRegistration') }}
            </span>
            <Toggle v-model="form.byRegistration" />
          </div>
          <template v-if="form.byRegistration">
            <DateRangePicker v-model:start-date="form.registeredFrom" v-model:end-date="form.registeredTo" />
            <p class="text-sm text-gray-700 dark:text-gray-300">{{ registrationSentence }}</p>
            <p class="input-hint">{{ t('admin.emailCampaigns.compose.timezoneHint', { tz: timezone }) }}</p>
          </template>
          <p v-else class="input-hint">{{ t('admin.emailCampaigns.compose.anyRegistration') }}</p>
        </div>

        <div class="space-y-2">
          <div class="flex items-center justify-between gap-3">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.emailCampaigns.compose.dormantOnly') }}
            </span>
            <Toggle v-model="form.dormantOnly" />
          </div>
          <template v-if="form.dormantOnly">
            <label class="flex flex-wrap items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model.number="form.inactiveDays"
                type="number"
                min="1"
                max="3650"
                step="1"
                class="input w-24"
              />
              {{ t('admin.emailCampaigns.compose.dormantSentence') }}
            </label>
            <p v-if="inactiveDaysInvalid" class="text-xs text-red-600 dark:text-red-400">
              {{ t('admin.emailCampaigns.compose.inactiveDaysInvalid') }}
            </p>
          </template>
          <p v-else class="input-hint">{{ t('admin.emailCampaigns.compose.anyActivity') }}</p>
        </div>

        <div>
          <label class="input-label" :for="ids.exclude">{{ t('admin.emailCampaigns.compose.exclude') }}</label>
          <textarea
            :id="ids.exclude"
            v-model="form.excludeText"
            rows="3"
            class="input font-mono text-sm"
            :placeholder="t('admin.emailCampaigns.compose.excludePlaceholder')"
          ></textarea>
          <p v-if="excludeLines.length > MAX_EXCLUDE" class="mt-1 text-xs text-red-600 dark:text-red-400">
            {{ t('admin.emailCampaigns.compose.excludeTooMany', { max: MAX_EXCLUDE }) }}
          </p>
          <p v-else-if="excludeSentence" class="input-hint">{{ excludeSentence }}</p>
        </div>

        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.emailCampaigns.compose.alwaysExcluded') }}</p>

        <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-800/60" aria-live="polite">
          <p
            :class="[
              'text-sm text-gray-800 transition-opacity dark:text-gray-200',
              previewLoading ? 'opacity-60' : '',
            ]"
            data-testid="email-campaign-count"
          >
            {{ countSentence }}
          </p>
          <button
            v-if="preview && previewIsCurrent && preview.matched > 0"
            type="button"
            class="mt-2 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
            @click="showAudienceList = true"
          >
            {{ t('admin.emailCampaigns.compose.viewList') }}
          </button>
        </div>

        <div>
          <label class="input-label" :for="ids.testTo">{{ t('admin.emailCampaigns.compose.testEmail') }}</label>
          <div class="flex flex-wrap gap-2">
            <input :id="ids.testTo" v-model="testTo" type="email" class="input min-w-0 flex-1" />
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="!canSendTest"
              data-testid="email-campaign-test-send"
              @click="sendTestEmail"
            >
              {{ sendingTest ? t('common.sending') : t('admin.emailCampaigns.compose.sendTest') }}
            </button>
          </div>
          <p v-if="testSentTo" class="input-hint">{{ t('admin.emailCampaigns.compose.testSentTo', { to: testSentTo }) }}</p>
        </div>
      </div>

      <!-- Preview -->
      <div class="flex min-w-0 flex-col gap-2">
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.emailCampaigns.compose.preview') }}
          </span>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="!hasContent || rendering"
            @click="renderNow"
          >
            <Icon name="refresh" size="sm" :class="rendering ? 'animate-spin' : ''" />
            {{ t('admin.emailCampaigns.compose.refreshPreview') }}
          </button>
        </div>
        <div class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
          <div class="bg-gray-100 p-3 dark:bg-dark-900">
            <iframe
              v-if="previewHtml"
              class="h-[36rem] w-full rounded-md border border-gray-200 bg-white dark:border-dark-700"
              sandbox=""
              :srcdoc="previewHtml"
              :title="t('admin.emailCampaigns.compose.preview')"
            ></iframe>
            <div
              v-else
              class="flex h-[36rem] items-center justify-center rounded-md border border-dashed border-gray-300 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400"
            >
              {{ t('admin.emailCampaigns.compose.previewEmpty') }}
            </div>
          </div>
        </div>
        <p v-if="renderError" class="text-xs text-red-600 dark:text-red-400">{{ renderError }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.emailCampaigns.compose.previewHint') }}</p>
      </div>
    </div>

    <template #footer>
      <div class="flex flex-wrap items-center justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!canSubmit"
          data-testid="email-campaign-send"
          @click="showConfirm = true"
        >
          {{ submitting ? t('admin.emailCampaigns.compose.starting') : sendLabel }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="showConfirm"
    :title="t('admin.emailCampaigns.compose.confirmTitle')"
    :message="confirmMessage"
    :confirm-text="t('admin.emailCampaigns.compose.confirmStart')"
    @confirm="confirmSend"
    @cancel="showConfirm = false"
  >
    <p
      v-if="!testSent"
      class="rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200"
      data-testid="email-campaign-no-test-warning"
    >
      {{ t('admin.emailCampaigns.compose.noTestWarning') }}
    </p>
  </ConfirmDialog>

  <BaseDialog
    :show="showAudienceList"
    :title="t('admin.emailCampaigns.compose.listTitle')"
    width="extra-wide"
    @close="showAudienceList = false"
  >
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">{{ countSentence }}</p>
      <DataTable :columns="listColumns" :data="preview?.items ?? []" :loading="previewLoading" row-key="user_id">
        <template #cell-created_at="{ value }">
          <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) }}</span>
        </template>
        <template #cell-last_activity_at="{ value }">
          <span class="text-sm text-gray-500 dark:text-dark-400">
            {{ value ? formatDateTime(value) : t('admin.emailCampaigns.compose.neverActive') }}
          </span>
        </template>
        <template #cell-actions="{ row }">
          <button type="button" class="btn btn-secondary btn-sm" @click="excludeUser(row)">
            {{ t('admin.emailCampaigns.compose.excludeOne') }}
          </button>
        </template>
      </DataTable>
      <Pagination
        v-if="preview && preview.total > 0"
        :page="listPage"
        :total="preview.total"
        :page-size="listPageSize"
        @update:page="handleListPage"
        @update:pageSize="handleListPageSize"
      />
    </div>
    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="showAudienceList = false">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <TotpStepUpDialog :controller="stepUp" />
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { isStepUpCancelled, useStepUp } from '@/composables/useStepUp'
import { formatDateTime, getBrowserTimeZone } from '@/utils/format'
import type { Column } from '@/components/common/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Pagination from '@/components/common/Pagination.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import * as api from './api'
import {
  campaignErrorMessage,
  estimateMinutes,
  formatCount,
  formatDuration,
  isAbortError,
  parseLines,
} from './campaignFormat'
import type { AudienceInput, Campaign, CampaignSettings, PreviewResult, PreviewUser } from './types'

const PLACEHOLDERS = ['{{username}}', '{{site_name}}'] as const
const MAX_EXCLUDE = 1000
const PREVIEW_DEBOUNCE_MS = 400
const RENDER_DEBOUNCE_MS = 600
const DEFAULT_INACTIVE_DAYS = 30

const props = defineProps<{
  show: boolean
  settings: CampaignSettings | null
  canCreate: boolean
  defaultTestEmail: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created', campaign: Campaign): void
}>()

const { t } = useI18n()
const appStore = useAppStore()
const stepUp = useStepUp()

const uid = useId()
const ids = {
  subject: `ec-subject-${uid}`,
  body: `ec-body-${uid}`,
  exclude: `ec-exclude-${uid}`,
  testTo: `ec-test-${uid}`,
}

const timezone = getBrowserTimeZone()

function localDate(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

function defaultRange(): { from: string; to: string } {
  const to = new Date()
  const from = new Date(to)
  from.setFullYear(from.getFullYear() - 1)
  return { from: localDate(from), to: localDate(to) }
}

// The form survives closing the dialog so an accidental close loses nothing;
// it is reset only after a campaign is created.
const form = reactive({
  subject: '',
  body: '',
  byRegistration: false,
  registeredFrom: '',
  registeredTo: '',
  dormantOnly: false,
  inactiveDays: DEFAULT_INACTIVE_DAYS as number,
  excludeText: '',
})

function resetForm() {
  form.subject = ''
  form.body = ''
  form.byRegistration = false
  form.registeredFrom = ''
  form.registeredTo = ''
  form.dormantOnly = false
  form.inactiveDays = DEFAULT_INACTIVE_DAYS
  form.excludeText = ''
  preview.value = null
  previewKey.value = ''
  previewHtml.value = ''
}

watch(
  () => form.byRegistration,
  (on) => {
    if (on && !form.registeredFrom && !form.registeredTo) {
      const range = defaultRange()
      form.registeredFrom = range.from
      form.registeredTo = range.to
    }
  },
)

const bodyRef = ref<HTMLTextAreaElement | null>(null)

async function insertPlaceholder(placeholder: string) {
  const el = bodyRef.value
  const start = el?.selectionStart ?? form.body.length
  const end = el?.selectionEnd ?? form.body.length
  form.body = form.body.slice(0, start) + placeholder + form.body.slice(end)
  await nextTick()
  if (el) {
    el.focus()
    const caret = start + placeholder.length
    el.setSelectionRange(caret, caret)
  }
}

// ===== Audience =====
const excludeLines = computed(() => parseLines(form.excludeText))

const inactiveDaysInvalid = computed(() => {
  if (!form.dormantOnly) return false
  const n = form.inactiveDays
  return !Number.isInteger(n) || n < 1 || n > 3650
})

const dateRangeInvalid = computed(
  () =>
    form.byRegistration &&
    !!form.registeredFrom &&
    !!form.registeredTo &&
    form.registeredFrom > form.registeredTo,
)

/** The audience to send, or null while the inputs are invalid. */
const audience = computed<AudienceInput | null>(() => {
  if (inactiveDaysInvalid.value || dateRangeInvalid.value) return null
  if (excludeLines.value.length > MAX_EXCLUDE) return null
  const out: AudienceInput = { timezone }
  if (form.byRegistration) {
    if (form.registeredFrom) out.registered_from = form.registeredFrom
    if (form.registeredTo) out.registered_to = form.registeredTo
  }
  if (form.dormantOnly) out.inactive_days = form.inactiveDays
  if (excludeLines.value.length > 0) out.exclude = excludeLines.value
  return out
})
const audienceKey = computed(() => (audience.value ? JSON.stringify(audience.value) : ''))

const registrationSentence = computed(() => {
  const from = form.registeredFrom
  const to = form.registeredTo
  if (from && to) return t('admin.emailCampaigns.compose.registrationRange', { from, to })
  if (from) return t('admin.emailCampaigns.compose.registrationFrom', { from })
  if (to) return t('admin.emailCampaigns.compose.registrationTo', { to })
  return t('admin.emailCampaigns.compose.anyRegistration')
})

// ===== Live count (/preview) =====
const preview = ref<PreviewResult | null>(null)
/** audienceKey of the request that produced `preview`. */
const previewKey = ref('')
const previewLoading = ref(false)
const previewError = ref('')
const listPage = ref(1)
const listPageSize = ref(Math.min(getPersistedPageSize(), api.MAX_PAGE_SIZE))
const showAudienceList = ref(false)

let previewTimer: ReturnType<typeof setTimeout> | null = null
let previewController: AbortController | null = null

const previewIsCurrent = computed(() => !!audienceKey.value && previewKey.value === audienceKey.value)

function clearPreviewTimer() {
  if (previewTimer !== null) {
    clearTimeout(previewTimer)
    previewTimer = null
  }
}

async function loadPreview(page: number) {
  clearPreviewTimer()
  const current = audience.value
  const key = audienceKey.value
  previewController?.abort()
  if (!current) {
    previewController = null
    previewLoading.value = false
    return
  }
  const controller = new AbortController()
  previewController = controller
  previewLoading.value = true
  try {
    const res = await api.previewAudience(
      { audience: current, page, page_size: listPageSize.value },
      { signal: controller.signal },
    )
    if (previewController !== controller) return
    preview.value = res
    previewKey.value = key
    listPage.value = res.page || page
    previewError.value = ''
  } catch (err) {
    if (previewController !== controller || isAbortError(err)) return
    previewError.value = campaignErrorMessage(err, t)
  } finally {
    if (previewController === controller) {
      previewLoading.value = false
      previewController = null
    }
  }
}

function schedulePreview() {
  clearPreviewTimer()
  previewTimer = setTimeout(() => {
    previewTimer = null
    void loadPreview(1)
  }, PREVIEW_DEBOUNCE_MS)
}

watch(audienceKey, () => {
  if (props.show) schedulePreview()
})

function handleListPage(page: number) {
  void loadPreview(page)
}

function handleListPageSize(size: number) {
  listPageSize.value = Math.min(size, api.MAX_PAGE_SIZE)
  void loadPreview(1)
}

function excludeUser(row: PreviewUser) {
  const value = row.email || String(row.user_id)
  const existing = new Set(excludeLines.value.map((line) => line.toLowerCase()))
  if (existing.has(value.toLowerCase()) || existing.has(String(row.user_id))) return
  const text = form.excludeText.replace(/\s+$/, '')
  form.excludeText = text ? `${text}\n${value}` : value
}

const excludeSentence = computed(() => {
  if (excludeLines.value.length === 0 || !preview.value || !previewIsCurrent.value) return ''
  const notFound = preview.value.exclude_not_found ?? []
  const resolved = formatCount(preview.value.exclude_resolved)
  if (notFound.length === 0) return t('admin.emailCampaigns.compose.excludeResolved', { n: resolved })
  return t('admin.emailCampaigns.compose.excludeResolvedNotFound', {
    n: resolved,
    m: formatCount(notFound.length),
    list: notFound.join(', '),
  })
})

const countSentence = computed(() => {
  if (!audience.value) return t('admin.emailCampaigns.compose.countInvalid')
  if (previewError.value && !previewLoading.value) return previewError.value
  const p = preview.value
  if (!p) return t('admin.emailCampaigns.compose.counting')
  const head = t('admin.emailCampaigns.compose.countMatched', { n: formatCount(p.matched) })
  const skipped = t('admin.emailCampaigns.compose.countSkipped', {
    noEmail: formatCount(p.skipped.no_valid_email),
    suppressed: formatCount(p.skipped.suppressed),
    excluded: formatCount(p.skipped.excluded),
    duplicate: formatCount(p.skipped.duplicate),
  })
  return `${head}${skipped}`
})

// ===== Server preview (/render) =====
const previewHtml = ref('')
const rendering = ref(false)
const renderError = ref('')
let renderTimer: ReturnType<typeof setTimeout> | null = null
let renderController: AbortController | null = null

const trimmedSubject = computed(() => form.subject.trim())
const hasContent = computed(() => trimmedSubject.value.length > 0 && form.body.trim().length > 0)

function clearRenderTimer() {
  if (renderTimer !== null) {
    clearTimeout(renderTimer)
    renderTimer = null
  }
}

async function renderNow() {
  clearRenderTimer()
  renderController?.abort()
  if (!hasContent.value) {
    renderController = null
    rendering.value = false
    previewHtml.value = ''
    return
  }
  const controller = new AbortController()
  renderController = controller
  rendering.value = true
  try {
    const res = await api.renderContent(
      { subject: trimmedSubject.value, body_markdown: form.body },
      { signal: controller.signal },
    )
    if (renderController !== controller) return
    previewHtml.value = res.html
    renderError.value = ''
  } catch (err) {
    if (renderController !== controller || isAbortError(err)) return
    renderError.value = campaignErrorMessage(err, t)
  } finally {
    if (renderController === controller) {
      rendering.value = false
      renderController = null
    }
  }
}

watch([() => form.subject, () => form.body], () => {
  if (!props.show) return
  clearRenderTimer()
  renderTimer = setTimeout(() => {
    renderTimer = null
    void renderNow()
  }, RENDER_DEBOUNCE_MS)
})

// ===== Test email =====
const testTo = ref('')
const sendingTest = ref(false)
/** Whether a test mail went out during this opening of the dialog. */
const testSent = ref(false)
const testSentTo = ref('')

const canSendTest = computed(() => hasContent.value && testTo.value.includes('@') && !sendingTest.value)

async function sendTestEmail() {
  if (!canSendTest.value) return
  sendingTest.value = true
  try {
    const res = await api.sendTest({
      subject: trimmedSubject.value,
      body_markdown: form.body,
      to: testTo.value.trim(),
    })
    testSent.value = true
    testSentTo.value = res.to || testTo.value.trim()
    appStore.showSuccess(t('admin.emailCampaigns.compose.testSentTo', { to: testSentTo.value }))
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    sendingTest.value = false
  }
}

// ===== Send =====
const showConfirm = ref(false)
const submitting = ref(false)

const canSubmit = computed(
  () =>
    props.canCreate &&
    hasContent.value &&
    !!preview.value &&
    previewIsCurrent.value &&
    !previewLoading.value &&
    preview.value.matched > 0 &&
    !submitting.value,
)

const sendLabel = computed(() => {
  if (preview.value && previewIsCurrent.value) {
    return t('admin.emailCampaigns.compose.sendTo', { n: formatCount(preview.value.matched) })
  }
  return t('admin.emailCampaigns.compose.send')
})

const confirmMessage = computed(() => {
  const count = preview.value?.matched ?? 0
  const s = props.settings
  const duration = s ? formatDuration(estimateMinutes(count, s.per_minute, s.per_day), t) : '-'
  return t('admin.emailCampaigns.compose.confirmMessage', {
    n: formatCount(count),
    subject: trimmedSubject.value,
    duration,
  })
})

async function confirmSend() {
  showConfirm.value = false
  const current = audience.value
  if (submitting.value || !current || !canSubmit.value) return
  submitting.value = true
  try {
    const campaign = await stepUp.run(() =>
      api.createCampaign({ subject: trimmedSubject.value, body_markdown: form.body, audience: current }),
    )
    appStore.showSuccess(t('admin.emailCampaigns.compose.started'))
    resetForm()
    emit('created', campaign)
  } catch (err) {
    if (isStepUpCancelled(err)) return
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    submitting.value = false
  }
}

// ===== Open / close =====
watch(
  () => props.show,
  (open) => {
    if (open) {
      testSent.value = false
      testSentTo.value = ''
      if (!testTo.value) testTo.value = props.defaultTestEmail
      void loadPreview(1)
      void renderNow()
    } else {
      showConfirm.value = false
      showAudienceList.value = false
      clearPreviewTimer()
      clearRenderTimer()
    }
  },
  { immediate: true },
)

const listColumns = computed<Column[]>(() => [
  { key: 'email', label: t('admin.emailCampaigns.compose.listColumns.email') },
  { key: 'username', label: t('admin.emailCampaigns.compose.listColumns.username') },
  { key: 'created_at', label: t('admin.emailCampaigns.compose.listColumns.registeredAt') },
  { key: 'last_activity_at', label: t('admin.emailCampaigns.compose.listColumns.lastActive') },
  { key: 'actions', label: t('admin.emailCampaigns.compose.listColumns.actions') },
])

onUnmounted(() => {
  clearPreviewTimer()
  clearRenderTimer()
  previewController?.abort()
  renderController?.abort()
})
</script>
