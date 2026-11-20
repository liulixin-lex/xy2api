<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- 统计卡、日期范围与图表；1024 以下收起成日期范围 + 一行摘要（日期也控制下面的列表） -->
      <UsageOverview :stats="usageStats" :e2e-state="usageStatsState">
        <template #range>
          <div class="flex items-center gap-2">
            <span class="sr-only text-sm font-medium text-gray-700 dark:text-gray-300 sm:not-sr-only">{{ t('admin.dashboard.timeRange') }}:</span>
            <DateRangePicker
              v-model:start-date="startDate"
              v-model:end-date="endDate"
              @change="onDateRangeChange"
            />
          </div>
        </template>
        <template #granularity>
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.dashboard.granularity') }}:</span>
            <div class="w-28">
              <Select v-model="granularity" :options="granularityOptions" @change="loadChartData" />
            </div>
          </div>
        </template>
        <template #charts>
          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <ModelDistributionChart
              v-model:source="modelDistributionSource"
              v-model:metric="modelDistributionMetric"
              :model-stats="requestedModelStats"
              :upstream-model-stats="upstreamModelStats"
              :mapping-model-stats="mappingModelStats"
              :loading="modelStatsLoading"
              :show-source-toggle="true"
              :show-metric-toggle="true"
              :start-date="startDate"
              :end-date="endDate"
              :filters="breakdownFilters"
            />
            <GroupDistributionChart
              v-model:metric="groupDistributionMetric"
              :group-stats="groupStats"
              :loading="chartsLoading"
              :show-metric-toggle="true"
              :start-date="startDate"
              :end-date="endDate"
              :filters="breakdownFilters"
            />
          </div>
          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <EndpointDistributionChart
              v-model:source="endpointDistributionSource"
              v-model:metric="endpointDistributionMetric"
              :endpoint-stats="inboundEndpointStats"
              :upstream-endpoint-stats="upstreamEndpointStats"
              :endpoint-path-stats="endpointPathStats"
              :loading="endpointStatsLoading"
              :show-source-toggle="true"
              :show-metric-toggle="true"
              :title="t('usage.endpointDistribution')"
              :start-date="startDate"
              :end-date="endDate"
              :filters="breakdownFilters"
            />
            <TokenUsageTrend :trend-data="trendData" :loading="chartsLoading" />
          </div>
        </template>
      </UsageOverview>
      <!-- 明细区：tab 栏 + 筛选 + 内容收进同一张卡片，消除割裂感；手机上贴到屏幕两边（同账号页的表格） -->
      <div class="card max-md:-mx-4 max-md:rounded-none max-md:border-x-0">
        <div class="flex flex-wrap items-center border-b border-gray-200 px-2 dark:border-dark-700 sm:px-4">
          <button
            v-for="tab in detailTabs"
            :key="tab.key"
            type="button"
            data-testid="usage-detail-tab"
            class="-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-3 text-sm font-medium transition-colors sm:px-4"
            :class="activeTab === tab.key
              ? 'border-primary-500 text-primary-600 dark:text-primary-400'
              : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 dark:text-gray-400 dark:hover:border-dark-500 dark:hover:text-gray-200'"
            @click="switchTab(tab.key)"
          >
            <Icon :name="tab.icon" size="sm" />
            {{ tab.label }}
          </button>
        </div>

        <UsageFilters v-model="filters" ref="usageFiltersRef" flat :mode="activeTab" class="border-b border-gray-100 dark:border-dark-700/50" :start-date="startDate" :end-date="endDate" :exporting="exporting" :model-options="modelNameOptions" @change="applyFilters" @refresh="refreshData" @reset="resetFilters" :can-cleanup="!adminAccess.isOperator" :can-export="has('data.export')" @cleanup="openCleanupDialog" @export="exportToExcel">
          <template v-if="activeTab === 'usage'" #status>
            <LiveStatusIndicator :status="liveStatus" :pause="liveUsage.pause" :resume="liveUsage.resume" :detail="t('admin.usage.liveDetail')" />
          </template>
          <!-- Phone toolbar: the count of the tab (and the sort while its column is hidden), the columns tool. The usage
               tab's count only when it is exact (the summary above has the requests either way). -->
          <template #info>
            <template v-if="activeTab !== 'ranking'">
              <span v-if="countShown" data-testid="usage-count">{{ t('common.table.count', { n: (activeTab === 'errors' ? errTotal : usageTotal).toLocaleString() }) }}</span>
              <template v-if="hiddenSort">
                <span v-if="countShown" class="table-toolbar__sep" aria-hidden="true">·</span>
                <TableSortHint :label="hiddenSort.label" :order="hiddenSort.order" />
              </template>
            </template>
          </template>
          <template #phone-tools>
            <UsageColumnSettings
              v-if="activeTab !== 'ranking'"
              variant="sheet"
              :columns="currentToggleableColumns"
              :is-visible="isCurrentColumnVisible"
              :at-default="currentColumnsAtDefault"
              @toggle="toggleCurrentColumn"
              @reset="resetCurrentColumns"
            />
          </template>
          <template v-if="hiddenSort" #sort-hint>
            <TableSortHint :label="hiddenSort.label" :order="hiddenSort.order" />
          </template>
          <template #after-reset>
            <UsageColumnSettings
              v-if="activeTab !== 'ranking'"
              :columns="currentToggleableColumns"
              :is-visible="isCurrentColumnVisible"
              :at-default="currentColumnsAtDefault"
              @toggle="toggleCurrentColumn"
              @reset="resetCurrentColumns"
            >
              <template #icon>
                <svg class="h-4 w-4 md:mr-1.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M9 4.5v15m6-15v15m-10.875 0h15.75c.621 0 1.125-.504 1.125-1.125V5.625c0-.621-.504-1.125-1.125-1.125H4.125C3.504 4.5 3 5.004 3 5.625v12.75c0 .621.504 1.125 1.125 1.125z" />
                </svg>
              </template>
            </UsageColumnSettings>
          </template>
        </UsageFilters>
        <UsageExportTasks v-if="has('data.export')" ref="exportPanel" scope="admin" @busy="exporting = $event" />

        <!-- overflow-clip, not hidden: the sticky anchor below must follow the page scroll -->
        <div v-show="activeTab === 'usage'" class="overflow-clip rounded-b-2xl">
          <UsageTable
            ref="usageTableRef"
            flat
            viewer="admin"
            :row-class="usageRowClass"
            :show-cache-hit-rate="adminSettingsStore.usageCacheHitRateEnabled"
            :show-token-speed="adminSettingsStore.usageTokenSpeedEnabled"
            show-fallback-origin
            show-end-to-end
            :data="usageRows"
            :loading="usageLoading"
            :columns="visibleColumns"
            :server-side-sort="true"
            :sort="tableSort"
            :scroll-label="t('usage.tabs.usage')"
            @sort="handleSort"
            @userClick="handleUserClick"
            @ipGeoBatchFailed="handleIpGeoBatchFailed"
          >
            <!-- "N new records" shows only while the list is away from its top (new records come in by themselves
                 there), so from 1024px it rides this zero-height anchor right above the table, under the IP lookup bar
                 (7j-4): in the header band while the list's top is in view, under the page header once it has
                 scrolled away. Below 1024px the anchor is no layer of its own and the pill, fixed under the top bar,
                 stacks as on the users and groups pages. -->
            <template #above-table>
              <div class="h-0 lg:sticky lg:top-16 lg:z-10">
                <PendingChangesPill :count="pending" :more="pendingMore" :label="newRecordsText" @merge="applyPending" />
              </div>
            </template>
          </UsageTable>
          <Pagination v-if="usageTotal > 0" :page="pagination.page" :total="usagePaginationTotal" :total-exact="usageTotalExact" :page-size="pagination.page_size" @update:page="handlePageChange" @update:pageSize="handlePageSizeChange" />
        </div>
        <div v-show="activeTab === 'errors'" class="overflow-hidden rounded-b-2xl">
          <OpsErrorLogTable
            flat
            layout="table"
            :rows="errRows" :total="errTotal" :loading="errLoading"
            :page="errPage" :page-size="errPageSize"
            :visible-column-keys="errVisibleColumnKeys"
            :sort-by="errSortBy"
            :sort-order="errSortOrder"
            :scroll-label="t('usage.tabs.errors')"
            user-clickable
            @userClick="handleUserClick"
            @openErrorDetail="openError"
            @sort="onErrSort"
            @update:page="onErrPage"
            @update:pageSize="onErrPageSize"
            @ipGeoBatchFailed="handleIpGeoBatchFailed" />
        </div>
        <!-- 懒挂载：首次切到该 tab 才请求排行数据，之后随筛选自动刷新 -->
        <div v-if="rankingMounted" v-show="activeTab === 'ranking'" class="overflow-hidden rounded-b-2xl">
          <UserTokenRanking
            ref="rankingRef"
            :start-date="startDate"
            :end-date="endDate"
            :filters="breakdownFilters"
            :model="filters.model"
            @select-user="handleRankingSelectUser"
          />
        </div>
      </div>
      <OpsErrorDetailModal v-model:show="showErrorModal" :error-id="selectedErrorId" :error-type="'request'" />
    </div>
  </AppLayout>
  <UsageCleanupDialog
    :show="cleanupDialogVisible"
    :filters="filters"
    :start-date="startDate"
    :end-date="endDate"
    @close="cleanupDialogVisible = false"
  />
  <!-- Balance history modal triggered from usage table user click -->
  <UserBalanceHistoryModal
    :show="showBalanceHistoryModal"
    :user="balanceHistoryUser"
    :hide-actions="true"
    @close="showBalanceHistoryModal = false; balanceHistoryUser = null"
  />
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { useAppStore } from '@/stores/app'; import { adminAPI } from '@/api/admin'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useLiveTable } from '@/composables/useLiveTable'
import { requestTypeToLegacyStream } from '@/utils/usageRequestType'
import AppLayout from '@/components/layout/AppLayout.vue'; import Pagination from '@/components/common/Pagination.vue'; import Select from '@/components/common/Select.vue'; import DateRangePicker from '@/components/common/DateRangePicker.vue'
import { useUsageStatsState } from './usageStatsState'
import UsageOverview from '@/components/admin/usage/UsageOverview.vue'; import UsageFilters from '@/components/admin/usage/UsageFilters.vue'
import UsageTable from '@/components/admin/usage/UsageTable.vue'; import { usageTotalIsExact } from '@/components/admin/usage/usageTotal'; import UsageExportTasks from '@/components/usage/UsageExportTasks.vue'
import UserTokenRanking from '@/components/admin/usage/UserTokenRanking.vue'
import UsageCleanupDialog from '@/components/admin/usage/UsageCleanupDialog.vue'
import UsageColumnSettings from '@/components/admin/usage/UsageColumnSettings.vue'
import TableSortHint from '@/components/common/TableSortHint.vue'
import { useColumnVisibility } from '@/composables/useColumnVisibility'
import UserBalanceHistoryModal from '@/components/admin/user/UserBalanceHistoryModal.vue'
import OpsErrorLogTable from '@/views/admin/ops/components/OpsErrorLogTable.vue'
import OpsErrorDetailModal from '@/views/admin/ops/components/OpsErrorDetailModal.vue'
import { listErrorLogs } from '@/api/admin/ops'
import type { OpsErrorLog } from '@/api/admin/ops'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'; import GroupDistributionChart from '@/components/charts/GroupDistributionChart.vue'; import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import EndpointDistributionChart from '@/components/charts/EndpointDistributionChart.vue'
import Icon from '@/components/icons/Icon.vue'
import LiveStatusIndicator from '@/components/common/LiveStatusIndicator.vue'
import PendingChangesPill from '@/components/common/PendingChangesPill.vue'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminUsageLog, TrendDataPoint, ModelStat, GroupStat, EndpointStat, AdminUser } from '@/types'; import type { AdminUsageStatsResponse, AdminUsageQueryParams } from '@/api/admin/usage'

const { t } = useI18n()
const appStore = useAppStore()
// 导出面板需要“导出业务数据”权限；清理使用记录只有超级管理员可以操作
const { has } = useAdminPermission('usage')
const adminAccess = useAdminAccessStore()
type DistributionMetric = 'tokens' | 'actual_cost'
type EndpointSource = 'inbound' | 'upstream' | 'path'
type ModelDistributionSource = 'requested' | 'upstream' | 'mapping'
const adminSettingsStore = useAdminSettingsStore()
const route = useRoute()
const usageStats = ref<AdminUsageStatsResponse | null>(null); const exporting = ref(false)
const trendData = ref<TrendDataPoint[]>([]); const requestedModelStats = ref<ModelStat[]>([]); const upstreamModelStats = ref<ModelStat[]>([]); const mappingModelStats = ref<ModelStat[]>([]); const groupStats = ref<GroupStat[]>([]); const chartsLoading = ref(false); const modelStatsLoading = ref(false); const granularity = ref<'day' | 'hour'>('hour')
const modelDistributionMetric = ref<DistributionMetric>('tokens')
const modelDistributionSource = ref<ModelDistributionSource>('requested')
const loadedModelSources = reactive<Record<ModelDistributionSource, boolean>>({
  requested: false,
  upstream: false,
  mapping: false,
})
const groupDistributionMetric = ref<DistributionMetric>('tokens')
const endpointDistributionMetric = ref<DistributionMetric>('tokens')
const endpointDistributionSource = ref<EndpointSource>('inbound')
const inboundEndpointStats = ref<EndpointStat[]>([])
const upstreamEndpointStats = ref<EndpointStat[]>([])
const endpointPathStats = ref<EndpointStat[]>([])
const endpointStatsLoading = ref(false)
let chartReqSeq = 0
let modelStatsReqSeq = 0
const cleanupDialogVisible = ref(false)
// Balance history modal state
const showBalanceHistoryModal = ref(false)
const balanceHistoryUser = ref<AdminUser | null>(null)

const breakdownFilters = computed(() => {
  const f: Record<string, any> = {}
  if (filters.value.user_id) f.user_id = filters.value.user_id
  if (filters.value.api_key_id) f.api_key_id = filters.value.api_key_id
  if (filters.value.account_id) f.account_id = filters.value.account_id
  if (filters.value.group_id) f.group_id = filters.value.group_id
  if (filters.value.request_type != null) f.request_type = filters.value.request_type
  if (filters.value.native_compaction_v2 != null) f.native_compaction_v2 = filters.value.native_compaction_v2
  if (filters.value.billing_type != null) f.billing_type = filters.value.billing_type
  return f
})

const modelNameOptions = computed(() =>
  Array.from(new Set(requestedModelStats.value.map((m) => m.model).filter(Boolean))).sort()
)

const handleUserClick = async (userId: number) => {
  try {
    const user = await adminAPI.users.getById(userId, true)
    balanceHistoryUser.value = user
    showBalanceHistoryModal.value = true
  } catch {
    appStore.showError(t('admin.usage.failedToLoadUser'))
  }
}

// Drill down from the per-user token ranking: scope the whole usage view to
// that user and jump to the usage-detail tab so the drill-down is visible.
const handleRankingSelectUser = (userId: number, email: string) => {
  filters.value = { ...filters.value, user_id: userId }
  usageFiltersRef.value?.setUserKeyword?.(email || '')
  activeTab.value = 'usage'
  applyFilters()
}

const granularityOptions = computed(() => [{ value: 'day', label: t('admin.dashboard.day') }, { value: 'hour', label: t('admin.dashboard.hour') }])
// Use local timezone to avoid UTC timezone issues
const formatLD = (d: Date) => {
  const year = d.getFullYear()
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
const getLast24HoursRangeDates = (): { start: string; end: string } => {
  const end = new Date()
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
  return {
    start: formatLD(start),
    end: formatLD(end)
  }
}
const getGranularityForRange = (start: string, end: string): 'day' | 'hour' => {
  const startTime = new Date(`${start}T00:00:00`).getTime()
  const endTime = new Date(`${end}T00:00:00`).getTime()
  const daysDiff = Math.ceil((endTime - startTime) / (1000 * 60 * 60 * 24))
  return daysDiff <= 1 ? 'hour' : 'day'
}
const defaultRange = getLast24HoursRangeDates()
const startDate = ref(defaultRange.start); const endDate = ref(defaultRange.end)
const filters = ref<AdminUsageQueryParams>({ user_id: undefined, model: undefined, group_id: undefined, request_type: undefined, native_compaction_v2: null, billing_type: null, start_date: startDate.value, end_date: endDate.value })
const pagination = reactive({ page: 1, page_size: getPersistedPageSize() })
const sortState = reactive({
  sort_by: 'created_at',
  sort_order: 'desc' as 'asc' | 'desc'
})
/** The sort the list is requested with: the table's headers follow it (DataTable's page-owned sort). */
const tableSort = computed(() => ({ key: sortState.sort_by, order: sortState.sort_order }))

const getSingleQueryValue = (value: string | null | Array<string | null> | undefined): string | undefined => {
  if (Array.isArray(value)) return value.find((item): item is string => typeof item === 'string' && item.length > 0)
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

const getNumericQueryValue = (value: string | null | Array<string | null> | undefined): number | undefined => {
  const raw = getSingleQueryValue(value)
  if (!raw) return undefined
  const parsed = Number(raw)
  return Number.isFinite(parsed) ? parsed : undefined
}

const applyRouteQueryFilters = () => {
  const queryStartDate = getSingleQueryValue(route.query.start_date)
  const queryEndDate = getSingleQueryValue(route.query.end_date)
  const queryUserId = getNumericQueryValue(route.query.user_id)
  const queryAccountId = getNumericQueryValue(route.query.account_id)

  if (queryStartDate) {
    startDate.value = queryStartDate
  }
  if (queryEndDate) {
    endDate.value = queryEndDate
  }

  filters.value = {
    ...filters.value,
    user_id: queryUserId,
    account_id: queryAccountId,
    start_date: startDate.value,
    end_date: endDate.value
  }
  granularity.value = getGranularityForRange(startDate.value, endDate.value)
}

// 链接带来的账号筛选（?tab=errors&account_id=）：回显账号名。
const loadRouteAccountFilterLabel = async () => {
  const requestedAccountId = filters.value.account_id
  if (!requestedAccountId) return
  try {
    const account = await adminAPI.accounts.getById(requestedAccountId)
    if (filters.value.account_id !== requestedAccountId) return
    usageFiltersRef.value?.setAccountKeyword?.(account.name || String(requestedAccountId))
  } catch {
    if (filters.value.account_id !== requestedAccountId) return
    usageFiltersRef.value?.setAccountKeyword?.(String(requestedAccountId))
  }
}

const loadRouteUserFilterLabel = async () => {
  const requestedUserId = filters.value.user_id
  if (!requestedUserId) return
  const userSearchRevision = usageFiltersRef.value?.getUserSearchRevision?.()

  const routeUserFilterIsCurrent = () => (
    filters.value.user_id === requestedUserId
    && usageFiltersRef.value?.getUserSearchRevision?.() === userSearchRevision
  )

  try {
    const user = await adminAPI.users.getById(requestedUserId, true)
    if (!routeUserFilterIsCurrent()) return
    usageFiltersRef.value?.setUserKeyword?.(user.email || String(requestedUserId))
  } catch {
    if (!routeUserFilterIsCurrent()) return
    usageFiltersRef.value?.setUserKeyword?.(String(requestedUserId))
  }
}

const onDateRangeChange = (range: { startDate: string; endDate: string; preset: string | null }) => {
  startDate.value = range.startDate
  endDate.value = range.endDate
  filters.value = {
    ...filters.value,
    start_date: range.startDate,
    end_date: range.endDate
  }
  granularity.value = getGranularityForRange(range.startDate, range.endDate)
  applyFilters()
}

const buildUsageListParams = (
  page: number,
  pageSize: number,
  exactTotal: boolean
): AdminUsageQueryParams => {
  const requestType = filters.value.request_type
  const legacyStream = requestType ? requestTypeToLegacyStream(requestType) : filters.value.stream
  return {
    page,
    page_size: pageSize,
    exact_total: exactTotal,
    ...filters.value,
    stream: legacyStream === null ? undefined : legacyStream,
    sort_by: sortState.sort_by,
    sort_order: sortState.sort_order
  }
}

// The list is a live table in log mode: every 5 s the current page is fetched again; on page 1, newest first, with the
// list's top in view, new records come in at the top (trimmed to the page size); otherwise they are only counted
// (liveUsage.pending / pendingMore, taken in by liveUsage.applyPending). Usage rows never change once written, so there
// is no per-row source. The stats cards are a 30 s page task that also runs after every successful load. On the errors
// and ranking tabs the list's 5 s check is held (the cards go on) and runs at once on the way back (spec 8.5a).
type DetailTab = 'usage' | 'errors' | 'ranking'
const activeTab = ref<DetailTab>('usage')
const usageTableRef = ref<InstanceType<typeof UsageTable> | null>(null)
const usageRowId = (row: AdminUsageLog) => `${row.source ?? 'usage'}:${row.id}`
// The user-wait card's state for the current filter (see usageStatsState.ts): the list and the stats requests report
// to it, each with its generation. The other cards keep the last stats answer until a new one arrives, as before.
const statsCard = useUsageStatsState(() => JSON.stringify(filters.value))
const usageStatsState = statsCard.phase
/** The list a page belongs to: filters, page size and sort. */
const usageList = () => JSON.stringify([filters.value, pagination.page_size, sortState.sort_by, sortState.sort_order])
/** The query a list answer is for: the list and the page. */
const usageQuery = () => JSON.stringify([usageList(), pagination.page])
/** The last list answer the table took, with what it was asked for (an active load's or the 5 s check's). */
const usageAnswer = ref<{ list: string; query: string; filters: AdminUsageQueryParams; page: number; pageSize: number; rows: number; total: number } | null>(null)
const liveUsage = useLiveTable<AdminUsageLog>({
  getId: usageRowId,
  fetchPage: async (signal) => {
    // The stats run only after an active list load succeeds: its failure means the current filter's stats are not
    // asked. The 5 s membership check uses the same request but starts no stats, so its failure says nothing about
    // them (fourth review). The engine sets `loading` before an active load asks, and runs no check during one.
    const list = liveUsage.loading.value ? statsCard.listStarted() : null
    const asked = { list: usageList(), query: usageQuery(), filters: { ...filters.value }, page: pagination.page, pageSize: pagination.page_size }
    try {
      const answer = await adminAPI.usage.list(buildUsageListParams(asked.page, asked.pageSize, false), { signal })
      // The engine aborts what a newer load replaces: an answer that comes back unaborted is the one it takes.
      if (!signal.aborted) usageAnswer.value = { ...asked, rows: answer.items.length, total: answer.total }
      return answer
    } catch (error) {
      if (!signal.aborted) list?.failed()
      throw error
    }
  },
  membershipIntervalMs: 5000,
  membershipActive: () => activeTab.value === 'usage',
  tail: {
    enabled: () => pagination.page === 1 && sortState.sort_by === 'created_at' && sortState.sort_order === 'desc'
      && (usageTableRef.value?.isAtTop?.() ?? true),
    pageSize: () => pagination.page_size
  },
  tasks: [{ name: 'stats', intervalMs: 30_000, run: (signal) => runStatsTask(signal) }]
})
const { rows: usageRows, loading: usageLoading, total: usageTotal, pending, pendingMore, status: liveStatus } = liveUsage
/** The answer for the query on screen; none while a new page, filter or sort is on its way, or after it failed. */
const currentUsageAnswer = computed(() => (usageAnswer.value?.query === usageQuery() ? usageAnswer.value : null))
/**
 * The usage total is a count (see usageTotalIsExact), judged on the answer for the query on screen only: an earlier
 * answer's total says nothing about this page or filter. The errors list always counts.
 */
const usageTotalExact = computed(() => {
  const answer = currentUsageAnswer.value
  return !!answer && usageTotalIsExact(answer.filters, answer)
})
const countShown = computed(() => activeTab.value === 'errors' || usageTotalExact.value)
/**
 * The pagination's total. While a page is on its way (or failed) it shows the page asked for as an open range, and
 * "next" goes no further than the last answer for this list showed pages for: never past a page not yet answered
 * (previous, and the retry, stay).
 */
const usagePaginationTotal = computed(() => {
  if (currentUsageAnswer.value) return usageTotal.value
  const size = pagination.page_size
  const answer = usageAnswer.value
  const known = answer && answer.list === usageList() ? Math.ceil(answer.total / size) : 0
  return Math.max(pagination.page, known) * size
})
// A page past the end (rows went since the pages were counted) comes back empty with only its offset as the total:
// back to the last page that answer allows, asked again (each step is a lower page, page 1 ends it).
watch(currentUsageAnswer, (answer) => {
  if (!answer || answer.page <= 1 || answer.rows > 0) return
  pagination.page = Math.min(answer.page - 1, Math.max(1, Math.ceil(answer.total / answer.pageSize)))
  void reloadUsage()
})
const newRecordsText = (n: string) => t('admin.usage.newRecords', { n })
const usageRowClass = (row: AdminUsageLog) =>
  liveUsage.transitioning.value || liveUsage.isDimmed(usageRowId(row)) ? 'opacity-60' : undefined
const logLoadError = (error: unknown) => console.error('Failed to load usage logs:', error)
const reloadUsage = () => liveUsage.reload().catch(logLoadError)
const applyPending = () => liveUsage.applyPending().catch(logLoadError)

// The endpoint distribution charts come from the stats answer but follow only page opens, filter changes and manual
// refreshes, not the 30 s beat (spec 8.5); values stay until new ones arrive.
let endpointStatsDue = true
let statsNoCache = false
const askEndpointStats = (noCache = false) => {
  endpointStatsDue = true
  endpointStatsLoading.value = true
  if (noCache) statsNoCache = true
}
const runStatsTask = async (signal: AbortSignal) => {
  const run = statsCard.statsStarted()
  const withEndpoints = endpointStatsDue
  const noCache = statsNoCache
  const requestType = filters.value.request_type
  const legacyStream = requestType ? requestTypeToLegacyStream(requestType) : filters.value.stream
  try {
    const s = await adminAPI.usage.getStats({
      ...filters.value,
      stream: legacyStream === null ? undefined : legacyStream,
      ...(noCache ? { nocache: 1 } : {}),
    })
    if (signal.aborted || !run.latest()) return // a load started: the filter may have changed
    if (noCache) statsNoCache = false
    usageStats.value = s
    run.loaded()
    if (!withEndpoints) return
    inboundEndpointStats.value = s.endpoints || []
    upstreamEndpointStats.value = s.upstream_endpoints || []
    endpointPathStats.value = s.endpoint_paths || []
    endpointStatsDue = false
    endpointStatsLoading.value = false
  } catch (error) {
    if (signal.aborted || !run.latest()) return
    run.failed()
    console.error('Failed to load usage stats:', error)
    if (withEndpoints) endpointStatsLoading.value = false
    throw error // the engine retries it on the back-off beat
  }
}

// 失效模型统计缓存:仅标记需要重取,保留旧数据直到新数据到达(避免刷新时图表闪空)。
const invalidateModelStatsCache = () => {
  loadedModelSources.requested = false
  loadedModelSources.upstream = false
  loadedModelSources.mapping = false
}

const loadModelStats = async (source: ModelDistributionSource, force = false) => {
  if (!force && loadedModelSources[source]) {
    return
  }

  const seq = ++modelStatsReqSeq
  modelStatsLoading.value = true
  try {
    const requestType = filters.value.request_type
    const legacyStream = requestType ? requestTypeToLegacyStream(requestType) : filters.value.stream
    const baseParams = {
      start_date: filters.value.start_date || startDate.value,
      end_date: filters.value.end_date || endDate.value,
      user_id: filters.value.user_id,
      model: filters.value.model,
      api_key_id: filters.value.api_key_id,
      account_id: filters.value.account_id,
      group_id: filters.value.group_id,
      request_type: requestType,
      stream: legacyStream === null ? undefined : legacyStream,
      native_compaction_v2: filters.value.native_compaction_v2,
      billing_type: filters.value.billing_type,
	  upstream_model_mismatch: filters.value.upstream_model_mismatch,
    }

    const response = await adminAPI.dashboard.getModelStats({ ...baseParams, model_source: source })

    if (seq !== modelStatsReqSeq) return

    const models = response.models || []
    if (source === 'requested') {
      requestedModelStats.value = models
    } else if (source === 'upstream') {
      upstreamModelStats.value = models
    } else {
      mappingModelStats.value = models
    }
    loadedModelSources[source] = true
  } catch (error) {
    if (seq !== modelStatsReqSeq) return
    console.error('Failed to load model stats:', error)
    if (source === 'requested') {
      requestedModelStats.value = []
    } else if (source === 'upstream') {
      upstreamModelStats.value = []
    } else {
      mappingModelStats.value = []
    }
    loadedModelSources[source] = false
  } finally {
    if (seq === modelStatsReqSeq) modelStatsLoading.value = false
  }
}

const loadChartData = async () => {
  const seq = ++chartReqSeq
  chartsLoading.value = true
  try {
    const requestType = filters.value.request_type
    const legacyStream = requestType ? requestTypeToLegacyStream(requestType) : filters.value.stream
    const snapshot = await adminAPI.dashboard.getSnapshotV2({
      start_date: filters.value.start_date || startDate.value,
      end_date: filters.value.end_date || endDate.value,
      granularity: granularity.value,
      user_id: filters.value.user_id,
      model: filters.value.model,
      api_key_id: filters.value.api_key_id,
      account_id: filters.value.account_id,
      group_id: filters.value.group_id,
      request_type: requestType,
      stream: legacyStream === null ? undefined : legacyStream,
      native_compaction_v2: filters.value.native_compaction_v2,
      billing_type: filters.value.billing_type,
	  upstream_model_mismatch: filters.value.upstream_model_mismatch,
      include_stats: false,
      include_trend: true,
      include_model_stats: false,
      include_group_stats: true,
      include_users_trend: false
    })
    if (seq !== chartReqSeq) return
    trendData.value = snapshot.trend || []
    groupStats.value = snapshot.groups || []
  } catch (error) { console.error('Failed to load chart data:', error) } finally { if (seq === chartReqSeq) chartsLoading.value = false }
}
const applyFilters = () => {
  statsCard.retry()
  pagination.page = 1
  invalidateModelStatsCache()
  usageTableRef.value?.scrollToTop?.()
  askEndpointStats()
  void reloadUsage()
  loadModelStats(modelDistributionSource.value, true)
  loadChartData()
  errPage.value = 1
  if (activeTab.value === 'errors') {
    loadAdminErrors()
  } else {
    errRows.value = []
  }
}
const refreshData = () => {
  statsCard.retry()
  void adminSettingsStore.fetch(true)
  invalidateModelStatsCache()
  askEndpointStats(true)
  void reloadUsage()
  loadModelStats(modelDistributionSource.value, true)
  loadChartData()
  if (activeTab.value === 'errors') loadAdminErrors()
  if (rankingMounted.value) rankingRef.value?.reload()
}
const resetFilters = () => {
  const range = getLast24HoursRangeDates()
  startDate.value = range.start
  endDate.value = range.end
  filters.value = { start_date: startDate.value, end_date: endDate.value, request_type: undefined, native_compaction_v2: null, billing_type: null, billing_mode: undefined, result: null }
  granularity.value = getGranularityForRange(startDate.value, endDate.value)
  applyFilters()
}
const handlePageChange = (p: number) => { pagination.page = p; void reloadUsage() }
const handlePageSizeChange = (s: number) => { pagination.page_size = s; pagination.page = 1; void reloadUsage() }
const handleSort = (key: string, order: 'asc' | 'desc') => {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  void reloadUsage()
}

const handleIpGeoBatchFailed = () => {
  appStore.showError(t('usage.ipGeo.batchFailed'))
}
const openCleanupDialog = () => { cleanupDialogVisible.value = true }
const exportPanel = ref<InstanceType<typeof UsageExportTasks> | null>(null)
const exportToExcel = async () => {
  // 导出的是账单记录（usage 日志），不含无账单请求，也不按请求结果筛选；不把 result 带过去，免得看似已筛。
  const params = buildUsageListParams(1, 100, false)
  delete params.result
  await exportPanel.value?.create({ ...params })
}

// Column visibility
const ALWAYS_VISIBLE = ['user', 'created_at']
const DEFAULT_HIDDEN_COLUMNS = ['reasoning_effort', 'request_id', 'upstream_request_id', 'user_agent']
// Below 768px (mobile list plan): user (frozen), account, model, tokens, cost, latency and time; the rest in the settings.
const NARROW_DEFAULT_HIDDEN_COLUMNS = [
  'api_key', 'endpoint', 'group', 'stream', 'billing_mode', 'ip_address', ...DEFAULT_HIDDEN_COLUMNS,
]
const HIDDEN_COLUMNS_KEY = 'usage-hidden-columns'
const NARROW_HIDDEN_COLUMNS_KEY = 'usage-hidden-columns-narrow'
const HIDDEN_COLUMNS_VERSION_KEY = 'usage-hidden-columns-version'
// 隐藏列版本链：每级只把当级新增列加入隐藏集，不重置用户已显式打开的列。
const HIDDEN_COLUMNS_PREV_VERSION = 'request-id-hidden-by-default'
const HIDDEN_COLUMNS_CURRENT_VERSION = 'upstream-request-id-hidden-by-default'

const allColumns = computed(() => [
  { key: 'user', label: t('admin.usage.user'), sortable: false },
  { key: 'api_key', label: t('usage.apiKeyFilter'), sortable: false },
  { key: 'account', label: t('admin.usage.account'), sortable: false },
  { key: 'model', label: t('usage.model'), sortable: true },
  { key: 'reasoning_effort', label: t('usage.reasoningEffort'), sortable: false },
  { key: 'endpoint', label: t('usage.endpoint'), sortable: false },
  { key: 'group', label: t('admin.usage.group'), sortable: false },
  { key: 'stream', label: t('usage.type'), sortable: false },
  { key: 'billing_mode', label: t('admin.usage.billingMode'), sortable: false },
  { key: 'tokens', label: t('usage.tokens'), sortable: false, class: 'text-right' },
  { key: 'cost', label: t('usage.cost'), sortable: false, class: 'text-right' },
  { key: 'latency', label: t('usage.latency'), sortable: false, class: 'text-right' },
  { key: 'created_at', label: t('usage.time'), sortable: true },
  { key: 'request_id', label: t('admin.usage.requestId'), sortable: false },
  { key: 'upstream_request_id', label: t('admin.usage.upstreamRequestId'), sortable: false },
  { key: 'user_agent', label: t('usage.userAgent'), sortable: false },
  { key: 'ip_address', label: t('admin.usage.ipAddress'), sortable: false }
])

const toggleableColumns = computed(() =>
  allColumns.value.filter(col => !ALWAYS_VISIBLE.includes(col.key))
)

/** The wide memory is the page's existing key: its version chain hides the columns added since it was saved. */
const migrateUsageColumns = (hidden: Set<string>) => {
  const savedVersion = localStorage.getItem(HIDDEN_COLUMNS_VERSION_KEY)
  if (savedVersion === HIDDEN_COLUMNS_CURRENT_VERSION) return false
  if (savedVersion !== HIDDEN_COLUMNS_PREV_VERSION) hidden.add('request_id')
  hidden.add('upstream_request_id')
  localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
  return true
}
// Nothing saved yet: the defaults are current, so a set saved later is not run through the chain again.
try {
  if (localStorage.getItem(HIDDEN_COLUMNS_KEY) === null) localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
} catch { /* storage unavailable: the defaults apply */ }

const usageColumns = useColumnVisibility({
  keys: computed(() => toggleableColumns.value.map(col => col.key)),
  wide: { storageKey: HIDDEN_COLUMNS_KEY, defaultHidden: DEFAULT_HIDDEN_COLUMNS, migrate: migrateUsageColumns },
  narrow: { storageKey: NARROW_HIDDEN_COLUMNS_KEY, defaultHidden: NARROW_DEFAULT_HIDDEN_COLUMNS },
})

const visibleColumns = computed(() =>
  allColumns.value.filter(col =>
    ALWAYS_VISIBLE.includes(col.key) || usageColumns.isVisible(col.key)
  )
)

// ---- 错误请求 tab 列设置(与用量明细同机制,独立存储) ----
const ERR_ALWAYS_VISIBLE = ['user', 'status', 'created_at', 'actions']
const ERR_DEFAULT_HIDDEN_COLUMNS = ['user_agent']
// Below 768px: user (frozen), category, status and time, then the details button; the rest in the settings.
const ERR_NARROW_DEFAULT_HIDDEN_COLUMNS = ['api_key', 'account', 'platform', 'model', 'endpoint', 'group', 'type', 'message', 'user_agent', 'client_ip']
const ERR_HIDDEN_COLUMNS_KEY = 'usage-error-hidden-columns'

// key 集合须与 OpsErrorLogTable 内部 allColumns 一致
const errAllColumns = computed(() => [
  { key: 'user', label: t('admin.ops.errorLog.user') },
  { key: 'api_key', label: t('admin.ops.errorLog.apiKey') },
  { key: 'account', label: t('admin.ops.errorLog.account') },
  { key: 'platform', label: t('admin.ops.errorLog.platform') },
  { key: 'model', label: t('admin.ops.errorLog.model') },
  { key: 'endpoint', label: t('admin.ops.errorLog.endpoint') },
  { key: 'group', label: t('admin.ops.errorLog.group') },
  { key: 'type', label: t('admin.ops.errorLog.type') },
  { key: 'category', label: t('usage.errors.category') },
  { key: 'status', label: t('admin.ops.errorLog.status') },
  { key: 'message', label: t('admin.ops.errorLog.message') },
  { key: 'created_at', label: t('admin.ops.errorLog.time') },
  { key: 'user_agent', label: t('usage.userAgent') },
  { key: 'client_ip', label: t('admin.ops.errorLog.ip') },
  { key: 'actions', label: t('admin.ops.errorLog.action') },
])

const errToggleableColumns = computed(() =>
  errAllColumns.value.filter(col => !ERR_ALWAYS_VISIBLE.includes(col.key))
)

// The errors tab keeps its own two memories; phones start from their own defaults.
const errorColumns = useColumnVisibility({
  keys: computed(() => errToggleableColumns.value.map(col => col.key)),
  wide: { storageKey: ERR_HIDDEN_COLUMNS_KEY, defaultHidden: ERR_DEFAULT_HIDDEN_COLUMNS },
  narrow: { storageKey: `${ERR_HIDDEN_COLUMNS_KEY}-narrow`, defaultHidden: ERR_NARROW_DEFAULT_HIDDEN_COLUMNS },
})

const errVisibleColumnKeys = computed(() =>
  errAllColumns.value
    .filter(col => ERR_ALWAYS_VISIBLE.includes(col.key) || errorColumns.isVisible(col.key))
    .map(col => col.key)
)

// 列设置按当前 tab 分发（两类日志各自两份记忆）
const currentColumns = computed(() => (activeTab.value === 'errors' ? errorColumns : usageColumns))
const currentToggleableColumns = computed(() =>
  activeTab.value === 'errors' ? errToggleableColumns.value : toggleableColumns.value
)
const isCurrentColumnVisible = (key: string) => currentColumns.value.isVisible(key)
const toggleCurrentColumn = (key: string) => currentColumns.value.toggle(key)
const resetCurrentColumns = () => currentColumns.value.reset()
const currentColumnsAtDefault = computed(() => currentColumns.value.atDefault.value)
/**
 * The tab's sort while its column is hidden (only the model column can be), named in the toolbar: the request keeps the
 * sort, so the headers show no arrow (the usage tab's sort, or the errors tab's in API keys, status_code = status).
 */
const hiddenSort = computed(() => {
  if (activeTab.value === 'ranking') return null
  const errors = activeTab.value === 'errors'
  const key = errors ? (errSortBy.value === 'status_code' ? 'status' : errSortBy.value) : sortState.sort_by
  const columns: { key: string; label: string }[] = errors ? errAllColumns.value : allColumns.value
  const column = columns.find((col) => col.key === key)
  const shown = errors ? errVisibleColumnKeys.value.includes(key) : visibleColumns.value.some((col) => col.key === key)
  if (!column || shown) return null
  return { label: column.label, order: (errors ? errSortOrder.value : sortState.sort_order) as 'asc' | 'desc' }
})

// Detail tabs
const detailTabs = computed(() => [
  { key: 'usage' as const, label: t('usage.tabs.usage'), icon: 'document' as const },
  { key: 'errors' as const, label: t('usage.tabs.errors'), icon: 'exclamationTriangle' as const },
  { key: 'ranking' as const, label: t('usage.tabs.ranking'), icon: 'chart' as const },
])
const usageFiltersRef = ref<InstanceType<typeof UsageFilters> | null>(null)
const rankingMounted = ref(false)
const rankingRef = ref<InstanceType<typeof UserTokenRanking> | null>(null)

const switchTab = (tab: DetailTab) => {
  activeTab.value = tab
  if (tab === 'errors' && errRows.value.length === 0) loadAdminErrors()
  if (tab === 'ranking') rankingMounted.value = true
}

// Error tab state
const errRows = ref<OpsErrorLog[]>([])
const errLoading = ref(false)
const errPage = ref(1)
const errPageSize = ref(20)
const errTotal = ref(0)
const errSortBy = ref('created_at')
const errSortOrder = ref<'asc' | 'desc'>('desc')
const showErrorModal = ref(false)
const selectedErrorId = ref<number | null>(null)

// 注意：'YYYY-MM-DDT00:00:00' 无时区后缀，按本地时区解析后再转 UTC——与页面其它日期处理语义一致，刻意如此，勿改成 'T00:00:00Z'
const toRFC3339 = (d: string | undefined, endOfDay = false): string | undefined =>
  d ? new Date(d + (endOfDay ? 'T23:59:59.999' : 'T00:00:00')).toISOString() : undefined

const loadAdminErrors = async () => {
  errLoading.value = true
  try {
    const resp = await listErrorLogs({
      page: errPage.value,
      page_size: errPageSize.value,
      view: 'all',
      start_time: toRFC3339(filters.value.start_date),
      end_time: toRFC3339(filters.value.end_date, true),
      user_id: filters.value.user_id ?? undefined,
      api_key_id: filters.value.api_key_id ?? undefined,
      account_id: filters.value.account_id ?? undefined,
      group_id: filters.value.group_id ?? undefined,
      model: filters.value.model || undefined,
      phase: filters.value.error_phase || undefined,
      category: filters.value.error_category || undefined,
      status_codes: filters.value.status_code != null ? String(filters.value.status_code) : undefined,
      sort_by: errSortBy.value,
      sort_order: errSortOrder.value,
    })
    errRows.value = resp.items
    errTotal.value = resp.total
  } catch (error) {
    console.error('Failed to load admin errors:', error)
    appStore.showError(t('usage.errors.failedToLoad'))
  } finally {
    errLoading.value = false
  }
}

const onErrSort = (sortBy: string, sortOrder: 'asc' | 'desc') => {
  errSortBy.value = sortBy
  errSortOrder.value = sortOrder
  errPage.value = 1
  loadAdminErrors()
}
const onErrPage = (p: number) => { errPage.value = p; loadAdminErrors() }
const onErrPageSize = (s: number) => { errPageSize.value = s; errPage.value = 1; loadAdminErrors() }
const openError = (id: number) => { selectedErrorId.value = id; showErrorModal.value = true }

onMounted(() => {
  void adminSettingsStore.fetch(true)
  applyRouteQueryFilters()
  void loadRouteUserFilterLabel()
  void loadRouteAccountFilterLabel()
  if (getSingleQueryValue(route.query.tab) === 'errors') switchTab('errors')
  askEndpointStats()
  void reloadUsage()
  loadModelStats(modelDistributionSource.value, true)
  window.setTimeout(() => {
    void loadChartData()
  }, 120)
})

watch(modelDistributionSource, (source) => {
  void loadModelStats(source)
})

defineExpose({ requestedModelStats, refreshData })
</script>
