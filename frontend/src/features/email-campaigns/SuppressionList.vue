<template>
  <TablePageLayout>
    <template #filters>
      <div class="space-y-3">
        <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('admin.emailCampaigns.suppressions.intro') }}</p>
        <div class="flex flex-wrap items-center gap-3">
          <div class="flex-1 sm:max-w-64">
            <input
              v-model="search"
              type="text"
              class="input"
              :placeholder="t('admin.emailCampaigns.suppressions.search')"
              @input="handleSearch"
            />
          </div>
          <div class="flex flex-1 flex-wrap items-center justify-end gap-2">
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="loading"
              :title="t('common.refresh')"
              @click="load()"
            >
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button type="button" class="btn btn-primary" @click="openAdd">
              <Icon name="plus" size="md" class="mr-1" />
              {{ t('admin.emailCampaigns.suppressions.add') }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <template #table>
      <DataTable :columns="columns" :data="items" :loading="loading" row-key="id">
        <template #cell-email="{ row }">
          <div class="min-w-0">
            <div class="font-medium text-gray-900 dark:text-white">{{ row.email }}</div>
            <div v-if="row.username || row.user_id" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{ row.username || `#${row.user_id}` }}
            </div>
          </div>
        </template>
        <template #cell-source="{ row }">
          <span v-if="row.source === 'unsubscribe'" class="text-sm text-gray-700 dark:text-gray-300">
            {{
              row.campaign_subject
                ? t('admin.emailCampaigns.suppressions.sourceUnsubscribeFrom', { subject: row.campaign_subject })
                : t('admin.emailCampaigns.suppressions.sourceUnsubscribe')
            }}
          </span>
          <span v-else class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('admin.emailCampaigns.suppressions.sourceAdmin') }}
          </span>
        </template>
        <template #cell-note="{ value }">
          <span class="block max-w-xs truncate text-sm text-gray-600 dark:text-gray-300" :title="value">
            {{ value || '-' }}
          </span>
        </template>
        <template #cell-created_at="{ value }">
          <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) }}</span>
        </template>
        <template #cell-actions="{ row }">
          <button
            type="button"
            class="btn btn-secondary btn-sm text-red-600 dark:text-red-400"
            :disabled="removingId === row.id"
            @click="removeTarget = row"
          >
            {{ t('admin.emailCampaigns.suppressions.remove') }}
          </button>
        </template>
        <template #empty>
          <EmptyState
            :title="t('admin.emailCampaigns.suppressions.emptyTitle')"
            :description="t('admin.emailCampaigns.suppressions.emptyDescription')"
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

  <BaseDialog :show="showAdd" :title="t('admin.emailCampaigns.suppressions.addTitle')" @close="showAdd = false">
    <form :id="formId" class="space-y-4" @submit.prevent="submitAdd">
      <div>
        <label class="input-label" :for="`${formId}-entries`">{{ t('admin.emailCampaigns.suppressions.entries') }}</label>
        <textarea
          :id="`${formId}-entries`"
          v-model="addForm.entries"
          rows="8"
          class="input font-mono text-sm"
          :placeholder="t('admin.emailCampaigns.compose.excludePlaceholder')"
        ></textarea>
        <p v-if="addLines.length > MAX_ENTRIES" class="mt-1 text-xs text-red-600 dark:text-red-400">
          {{ t('admin.emailCampaigns.suppressions.tooMany', { max: MAX_ENTRIES }) }}
        </p>
      </div>
      <div>
        <label class="input-label" :for="`${formId}-note`">{{ t('admin.emailCampaigns.suppressions.note') }}</label>
        <input :id="`${formId}-note`" v-model="addForm.note" type="text" maxlength="200" class="input" />
      </div>
      <div
        v-if="addResult"
        class="rounded-lg bg-gray-50 px-3 py-2 text-sm text-gray-700 dark:bg-dark-800/60 dark:text-gray-200"
        aria-live="polite"
      >
        <p>
          {{
            t('admin.emailCampaigns.suppressions.addResult', {
              added: formatCount(addResult.added),
              already: formatCount(addResult.already),
            })
          }}
        </p>
        <p v-if="addResult.not_found.length > 0" class="mt-1 text-amber-700 dark:text-amber-300">
          {{
            t('admin.emailCampaigns.suppressions.addNotFound', {
              n: formatCount(addResult.not_found.length),
              list: addResult.not_found.join(', '),
            })
          }}
        </p>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="showAdd = false">{{ t('common.close') }}</button>
        <button type="submit" :form="formId" class="btn btn-primary" :disabled="!canSubmitAdd">
          {{ adding ? t('common.saving') : t('admin.emailCampaigns.suppressions.add') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="removeTarget !== null"
    :title="t('admin.emailCampaigns.suppressions.removeTitle')"
    :message="removeMessage"
    :confirm-text="t('admin.emailCampaigns.suppressions.remove')"
    :danger="removeTarget?.source === 'unsubscribe'"
    @confirm="confirmRemove"
    @cancel="removeTarget = null"
  />
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'
import type { Column } from '@/components/common/types'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import * as api from './api'
import { campaignErrorMessage, formatCount, isAbortError, parseLines } from './campaignFormat'
import type { AddSuppressionsResult, Suppression } from './types'

const MAX_ENTRIES = 500

const { t } = useI18n()
const appStore = useAppStore()
const formId = `ec-suppress-${useId()}`

const items = ref<Suppression[]>([])
const loading = ref(false)
const search = ref('')
const pagination = reactive({
  page: 1,
  page_size: Math.min(getPersistedPageSize(), api.MAX_PAGE_SIZE),
  total: 0,
})

const columns = computed<Column[]>(() => [
  { key: 'email', label: t('admin.emailCampaigns.suppressions.columns.email') },
  { key: 'source', label: t('admin.emailCampaigns.suppressions.columns.source') },
  { key: 'note', label: t('admin.emailCampaigns.suppressions.columns.note') },
  { key: 'created_at', label: t('admin.emailCampaigns.suppressions.columns.createdAt') },
  { key: 'actions', label: t('admin.emailCampaigns.suppressions.columns.actions') },
])

let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const res = await api.listSuppressions(
      { search: search.value.trim() || undefined, page: pagination.page, page_size: pagination.page_size },
      { signal: current.signal },
    )
    if (controller !== current) return
    items.value = res.items ?? []
    pagination.total = res.total
  } catch (err) {
    if (controller !== current || isAbortError(err)) return
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    if (controller === current) {
      loading.value = false
      controller = null
    }
  }
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
function handleSearch() {
  if (searchTimer !== null) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchTimer = null
    pagination.page = 1
    void load()
  }, 300)
}

function handlePageChange(page: number) {
  pagination.page = page
  void load()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = Math.min(size, api.MAX_PAGE_SIZE)
  pagination.page = 1
  void load()
}

// ===== Add =====
const showAdd = ref(false)
const adding = ref(false)
const addForm = reactive({ entries: '', note: '' })
const addResult = ref<AddSuppressionsResult | null>(null)
const addLines = computed(() => parseLines(addForm.entries))
const canSubmitAdd = computed(
  () => !adding.value && addLines.value.length > 0 && addLines.value.length <= MAX_ENTRIES,
)

function openAdd() {
  addResult.value = null
  showAdd.value = true
}

async function submitAdd() {
  if (!canSubmitAdd.value) return
  adding.value = true
  try {
    const note = addForm.note.trim()
    const res = await api.addSuppressions({ entries: addLines.value, ...(note ? { note } : {}) })
    addResult.value = { ...res, not_found: res.not_found ?? [] }
    addForm.entries = addResult.value.not_found.join('\n')
    pagination.page = 1
    void load()
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    adding.value = false
  }
}

// ===== Remove =====
const removeTarget = ref<Suppression | null>(null)
const removingId = ref<number | null>(null)

const removeMessage = computed(() => {
  const target = removeTarget.value
  if (!target) return ''
  if (target.source === 'unsubscribe') return t('admin.emailCampaigns.suppressions.removeUnsubscribeWarning')
  return t('admin.emailCampaigns.suppressions.removeConfirm', { email: target.email })
})

async function confirmRemove() {
  const target = removeTarget.value
  removeTarget.value = null
  if (!target) return
  removingId.value = target.id
  try {
    await api.removeSuppression(target.id)
    appStore.showSuccess(t('admin.emailCampaigns.suppressions.removed'))
    void load()
  } catch (err) {
    appStore.showError(campaignErrorMessage(err, t))
  } finally {
    removingId.value = null
  }
}

onMounted(() => {
  void load()
})

onUnmounted(() => {
  if (searchTimer !== null) clearTimeout(searchTimer)
  controller?.abort()
})
</script>
