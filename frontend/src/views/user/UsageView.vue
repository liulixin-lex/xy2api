<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- 统计卡、日期范围与图表；1024 以下收起成日期范围 + 一行摘要（日期也控制下面的列表） -->
      <UsageOverview :stats="usageStats" :show-account-cost="false" :strike-standard-cost="true">
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
              v-model:metric="modelDistributionMetric"
              :model-stats="requestedModelStats"
              :loading="modelStatsLoading"
              :show-source-toggle="false"
              :show-metric-toggle="true"
              :enable-breakdown="false"
              :show-account-cost="false"
              :start-date="startDate"
              :end-date="endDate"
            />
            <GroupDistributionChart
              v-model:metric="groupDistributionMetric"
              :group-stats="groupStats"
              :loading="chartsLoading"
              :show-metric-toggle="true"
              :enable-breakdown="false"
              :show-account-cost="false"
              :start-date="startDate"
              :end-date="endDate"
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
              :show-source-toggle="false"
              :show-metric-toggle="true"
              :enable-breakdown="false"
              :title="t('usage.endpointDistribution')"
              :start-date="startDate"
              :end-date="endDate"
            />
            <TokenUsageTrend :trend-data="trendData" :loading="chartsLoading" />
          </div>
        </template>
      </UsageOverview>

      <div class="card p-4 sm:p-6">
        <!-- The tab's fields, defined once: in the row from 768px, in the phone toolbar's bottom sheet below it. -->
        <DefineFields>
          <template v-if="activeTab === 'errors'">
            <div class="w-full sm:w-auto sm:min-w-[220px]">
              <label class="input-label">{{ t('usage.errors.keyName') }}</label>
              <Select v-model="errorFilter.api_key_id" :options="errorKeyOptions" @change="applyErrorFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[220px]">
              <label class="input-label">{{ t('usage.errors.model') }}</label>
              <Select
                v-model="errorFilter.model"
                :options="errorModelOptions"
                searchable
                creatable
                clearable
                :placeholder="t('usage.errors.modelPlaceholder')"
                @change="applyErrorFilters"
              />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[200px]">
              <label class="input-label">{{ t('usage.errors.category') }}</label>
              <Select v-model="errorFilter.category" :options="errorCategoryOptions" @change="applyErrorFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[180px]">
              <label class="input-label">{{ t('usage.errors.status') }}</label>
              <Select v-model="errorFilter.status_code" :options="errorStatusOptions" @change="applyErrorFilters" />
            </div>
          </template>
          <template v-else>
            <div class="w-full sm:w-auto sm:min-w-[220px]">
              <label class="input-label">{{ t('usage.apiKeyFilter') }}</label>
              <Select v-model="filters.api_key_id" :options="apiKeyOptions" @change="applyFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[220px]">
              <label class="input-label">{{ t('usage.model') }}</label>
              <Select v-model="filters.model" :options="modelOptions" searchable @change="applyFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[200px]">
              <label class="input-label">{{ t('admin.usage.group') }}</label>
              <Select v-model="filters.group_id" :options="groupOptions" searchable @change="applyFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[180px]">
              <label class="input-label">{{ t('usage.type') }}</label>
              <Select v-model="filters.request_type" :options="requestTypeOptions" @change="applyFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[180px]">
              <label class="input-label">{{ t('usage.compactionFilter') }}</label>
              <Select v-model="filters.native_compaction_v2" :options="compactionOptions" @change="applyFilters" />
            </div>
            <div v-if="subscriptionFeatureEnabled" class="w-full sm:w-auto sm:min-w-[200px]">
              <label class="input-label">{{ t('admin.usage.billingType') }}</label>
              <Select v-model="filters.billing_type" :options="billingTypeOptions" @change="applyFilters" />
            </div>
            <div class="w-full sm:w-auto sm:min-w-[200px]">
              <label class="input-label">{{ t('admin.usage.billingMode') }}</label>
              <Select v-model="filters.billing_mode" :options="billingModeOptions" @change="applyFilters" />
            </div>
          </template>
        </DefineFields>

        <!-- Below 768px (mobile list plan): "Filters n" opens the fields in a bottom sheet (applied at once), the chosen
             ones as removable tags; the count, refresh, columns and "more" (reset, export) on a row. -->
        <TableToolbar v-if="phone" filter-sheet :filter-count="filterTags.length" data-testid="usage-filters-phone" @clear-filters="clearAllFilters">
          <template #sheet>
            <div class="grid gap-4"><ReuseFields /></div>
          </template>
          <template #tags>
            <UsageFilterTags :tags="filterTags" @remove="removeFilterTag" />
          </template>
          <template #info>
            <span data-testid="usage-count">{{ t('common.table.count', { n: (activeTab === 'errors' ? errorTotal : pagination.total).toLocaleString() }) }}</span>
            <template v-if="activeTab === 'usage' && hiddenSort">
              <span class="table-toolbar__sep" aria-hidden="true">·</span>
              <TableSortHint :label="hiddenSort.label" :order="hiddenSort.order" />
            </template>
          </template>
          <template #actions>
            <button
              type="button"
              class="table-toolbar__icon-btn table-toolbar__icon"
              :aria-label="t('common.refresh')"
              :disabled="activeTab === 'errors' ? errorLoading : loading"
              data-testid="usage-refresh"
              @click="refreshData"
            >
              <Icon name="refresh" size="sm" />
            </button>
            <UsageColumnSettings
              variant="sheet"
              :columns="currentToggleableColumns"
              :is-visible="isCurrentColumnVisible"
              :at-default="currentColumnsAtDefault"
              @toggle="toggleCurrentColumn"
              @reset="resetCurrentColumns"
            />
            <button
              type="button"
              class="table-toolbar__icon-btn table-toolbar__icon"
              aria-haspopup="dialog"
              :aria-label="t('common.more')"
              data-testid="usage-more"
              @click="moreOpen = true"
            >
              <Icon name="more" size="sm" />
            </button>
          </template>
        </TableToolbar>
        <BottomSheet v-if="phone" :show="moreOpen" :title="t('common.more')" test-id="usage-more-sheet" @close="moreOpen = false">
          <div class="grid gap-3">
            <button type="button" class="btn btn-secondary w-full" @click="moreOpen = false; resetFilters()">{{ t('common.reset') }}</button>
            <button v-if="activeTab !== 'errors'" type="button" class="btn btn-primary w-full" :disabled="exporting" @click="moreOpen = false; exportToCSV()">
              {{ exporting ? t('usage.exporting') : t('usage.exportCsv') }}
            </button>
          </div>
        </BottomSheet>

        <div v-if="!phone" class="flex flex-wrap items-end justify-between gap-4">
          <div class="flex flex-1 flex-wrap items-end gap-4 max-lg:basis-full">
            <ReuseFields />
          </div>

          <div class="flex w-full flex-wrap items-center justify-end gap-3 sm:ml-auto sm:w-auto">
            <button type="button" @click="refreshData" :disabled="activeTab === 'errors' ? errorLoading : loading" class="btn btn-secondary">
              {{ t('common.refresh') }}
            </button>
            <button type="button" @click="resetFilters" class="btn btn-secondary">
              {{ t('common.reset') }}
            </button>
            <UsageColumnSettings
              :columns="currentToggleableColumns"
              :is-visible="isCurrentColumnVisible"
              :at-default="currentColumnsAtDefault"
              @toggle="toggleCurrentColumn"
              @reset="resetCurrentColumns"
            />
            <button v-if="activeTab !== 'errors'" type="button" @click="exportToCSV" :disabled="exporting" class="btn btn-primary">
              {{ exporting ? t('usage.exporting') : t('usage.exportCsv') }}
            </button>
          </div>
        </div>
      </div>

      <UsageExportTasks ref="exportPanel" scope="user" @busy="exporting = $event" />

      <div v-if="errorViewEnabled" class="flex gap-2 border-b border-gray-200 dark:border-dark-700">
        <button class="tab" :class="{ 'tab-active': activeTab === 'usage' }" @click="activeTab = 'usage'">
          {{ t('usage.tabs.usage') }}
        </button>
        <button class="tab" :class="{ 'tab-active': activeTab === 'errors' }" @click="switchToErrors">
          {{ t('usage.tabs.errors') }}
        </button>
      </div>

      <template v-if="activeTab === 'usage'">
        <UsageTable
          :data="usageLogs"
          :loading="loading"
          :columns="visibleColumns"
          :server-side-sort="true"
          :show-account-billing="false"
          :show-upstream-endpoint="false"
          :show-cache-hit-rate="appStore.cachedPublicSettings?.user_usage_cache_hit_rate_enabled === true"
          :show-token-speed="appStore.cachedPublicSettings?.user_usage_token_speed_enabled === true"
          default-sort-key="created_at"
          default-sort-order="desc"
          @sort="handleSort"
          @ipGeoBatchFailed="handleIpGeoBatchFailed"
        />

        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>

      <UserErrorRequestsTable
        v-else-if="errorViewEnabled"
        :rows="errorRows"
        :total="errorTotal"
        :loading="errorLoading"
        :page="errorPage"
        :page-size="errorPageSize"
        :visible-column-keys="errVisibleColumnKeys"
        @sort="onErrorSort"
        @update:page="onErrorPage"
        @update:pageSize="onErrorPageSize"
        @ipGeoBatchFailed="handleIpGeoBatchFailed"
      />
    </div>
  </AppLayout>

</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'
import { keysAPI, usageAPI, userGroupsAPI } from '@/api'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import UsageOverview from '@/components/admin/usage/UsageOverview.vue'
import UsageColumnSettings from '@/components/admin/usage/UsageColumnSettings.vue'
import UsageFilterTags, { type UsageFilterTag } from '@/components/admin/usage/UsageFilterTags.vue'
import TableToolbar from '@/components/layout/TableToolbar.vue'
import BottomSheet from '@/components/common/BottomSheet.vue'
import TableSortHint from '@/components/common/TableSortHint.vue'
import { usePhoneViewport } from '@/composables/useTableViewport'
import { createReusableTemplate } from '@vueuse/core'
import { useColumnVisibility } from '@/composables/useColumnVisibility'
import UsageTable from '@/components/admin/usage/UsageTable.vue'
import UsageExportTasks from '@/components/usage/UsageExportTasks.vue'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'
import GroupDistributionChart from '@/components/charts/GroupDistributionChart.vue'
import EndpointDistributionChart from '@/components/charts/EndpointDistributionChart.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import Icon from '@/components/icons/Icon.vue'
import UserErrorRequestsTable from '@/components/user/UserErrorRequestsTable.vue'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { requestTypeToLegacyStream } from '@/utils/usageRequestType'
import type {
  ApiKey,
  EndpointStat,
  Group,
  GroupStat,
  ModelStat,
  TrendDataPoint,
  UsageLog,
  UsageQueryParams,
  UsageStatsResponse,
  UserErrorRequest,
} from '@/types'
import type { Column } from '@/components/common/types'
import { COMMON_ERROR_STATUS_CODES } from '@/utils/errorBadges'

const { t } = useI18n()
const appStore = useAppStore()

type DistributionMetric = 'tokens' | 'actual_cost'
type EndpointSource = 'inbound' | 'upstream' | 'path'

const usageStats = ref<UsageStatsResponse | null>(null)
const usageLogs = ref<UsageLog[]>([])
const trendData = ref<TrendDataPoint[]>([])
const requestedModelStats = ref<ModelStat[]>([])
const groupStats = ref<GroupStat[]>([])
const inboundEndpointStats = ref<EndpointStat[]>([])
const upstreamEndpointStats = ref<EndpointStat[]>([])
const endpointPathStats = ref<EndpointStat[]>([])

const loading = ref(false)
const chartsLoading = ref(false)
const modelStatsLoading = ref(false)
const endpointStatsLoading = ref(false)
const exporting = ref(false)
const errorRows = ref<UserErrorRequest[]>([])
const errorLoading = ref(false)
const errorPage = ref(1)
const errorPageSize = ref(20)
const errorSortBy = ref('created_at')
const errorSortOrder = ref<'asc' | 'desc'>('desc')
const errorTotal = ref(0)
const errorFilter = ref<{ model: string | null; category: string; api_key_id: number | null; status_code: number | null }>({
  model: '',
  category: '',
  api_key_id: null,
  status_code: null,
})

const errorKeyOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allKeys') },
  ...apiKeys.value.map((k) => ({ value: k.id, label: k.name })),
])

// 模型候选取自当前已加载错误中出现过的模型；creatable 允许输入任意片段做后端模糊。
const errorModelOptions = computed<SelectOption[]>(() => {
  const seen = new Set<string>()
  const opts: SelectOption[] = []
  for (const r of errorRows.value) {
    if (r.model && !seen.has(r.model)) {
      seen.add(r.model)
      opts.push({ value: r.model, label: r.model })
    }
  }
  return opts
})

const errorCategoryCodes = ['auth', 'rate_limit', 'quota', 'invalid_request', 'service_unavailable', 'upstream', 'internal', 'cyber']

const errorCategoryOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('usage.errors.allCategories') },
  ...errorCategoryCodes.map((c) => ({ value: c, label: t('usage.errors.categories.' + c) })),
])

// 状态码候选用固定常用列表(与管理端 UsageFilters 共用常量),不受当前页数据限制:
// 后端 status_code 过滤对全量生效,若只列当前页出现过的码,用户就选不到仅在后续页的码。
const errorStatusOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allStatuses') },
  ...COMMON_ERROR_STATUS_CODES.map((c) => ({ value: c, label: String(c) })),
])

const applyErrorFilters = () => {
  errorPage.value = 1
  void loadErrors()
}

let abortController: AbortController | null = null
let chartReqSeq = 0
let statsReqSeq = 0
let modelStatsReqSeq = 0

const formatLocalDate = (date: Date): string =>
  `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`

const getLast24HoursRangeDates = () => {
  const end = new Date()
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
  return { start: formatLocalDate(start), end: formatLocalDate(end) }
}

const getGranularityForRange = (start: string, end: string): 'day' | 'hour' => {
  const startTime = new Date(`${start}T00:00:00`).getTime()
  const endTime = new Date(`${end}T00:00:00`).getTime()
  return Math.ceil((endTime - startTime) / (1000 * 60 * 60 * 24)) <= 1 ? 'hour' : 'day'
}

const defaultRange = getLast24HoursRangeDates()
const startDate = ref(defaultRange.start)
const endDate = ref(defaultRange.end)
const granularity = ref<'day' | 'hour'>(getGranularityForRange(startDate.value, endDate.value))

const modelDistributionMetric = ref<DistributionMetric>('tokens')
const groupDistributionMetric = ref<DistributionMetric>('tokens')
const endpointDistributionMetric = ref<DistributionMetric>('tokens')
const endpointDistributionSource = ref<EndpointSource>('inbound')
const activeTab = ref<'usage' | 'errors'>('usage')
const errorViewEnabled = computed(() => appStore.cachedPublicSettings?.allow_user_view_error_requests ?? false)

const filters = ref<UsageQueryParams>({
  start_date: startDate.value,
  end_date: endDate.value,
  request_type: undefined,
  native_compaction_v2: null,
  billing_type: null,
  billing_mode: null,
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
})
const sortState = reactive({
  sort_by: 'created_at',
  sort_order: 'desc' as 'asc' | 'desc',
})

const granularityOptions = computed<SelectOption[]>(() => [
  { value: 'day', label: t('admin.dashboard.day') },
  { value: 'hour', label: t('admin.dashboard.hour') },
])
const requestTypeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'live', label: t('usage.live') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'sync', label: t('usage.sync') },
])
const compactionOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.allCompactionTypes') },
  { value: true, label: t('usage.compactionOnly') },
])
// 订阅功能关闭后只剩余额计费，「计费类型」筛选（余额/订阅）失去意义，整块隐藏。
const subscriptionFeatureEnabled = computed(() => resolveFeatureFlag(appStore.cachedPublicSettings, FeatureFlags.subscription))
const billingTypeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allBillingTypes') },
  { value: 0, label: t('admin.usage.billingTypeBalance') },
  { value: 1, label: t('admin.usage.billingTypeSubscription') },
])
const billingModeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allBillingModes') },
  { value: 'token', label: t('admin.usage.billingModeToken') },
  { value: 'per_request', label: t('admin.usage.billingModePerRequest') },
  { value: 'image', label: t('admin.usage.billingModeImage') },
  { value: 'video', label: t('admin.usage.billingModeVideo') },
])

const apiKeys = ref<ApiKey[]>([])
const groups = ref<Group[]>([])
const modelOptionValues = ref<string[]>([])

const apiKeyOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.allApiKeys') },
  ...apiKeys.value.map((key) => ({ value: key.id, label: key.name })),
])
const groupOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allGroups') },
  ...groups.value.map((group) => ({ value: group.id, label: group.name })),
])
const modelOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allModels') },
  ...modelOptionValues.value.map((model) => ({ value: model, label: model })),
])

const normalizedFilters = computed<UsageQueryParams>(() => {
  const requestType = filters.value.request_type
  const legacyStream = requestType ? requestTypeToLegacyStream(requestType) : filters.value.stream
  return {
    ...filters.value,
    start_date: startDate.value,
    end_date: endDate.value,
    stream: legacyStream === null ? undefined : legacyStream,
  }
})

const buildUsageListParams = (page: number, pageSize: number): UsageQueryParams => ({
  page,
  page_size: pageSize,
  ...normalizedFilters.value,
  sort_by: sortState.sort_by,
  sort_order: sortState.sort_order,
})

const loadLogs = async () => {
  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  loading.value = true
  try {
    const res = await usageAPI.query(buildUsageListParams(pagination.page, pagination.page_size), {
      signal: controller.signal,
    })
    if (!controller.signal.aborted) {
      usageLogs.value = res.items
      pagination.total = res.total
    }
  } catch (error: any) {
    if (error?.name !== 'AbortError' && error?.code !== 'ERR_CANCELED') {
      appStore.showError(t('usage.failedToLoad'))
    }
  } finally {
    if (abortController === controller) loading.value = false
  }
}

const loadStats = async () => {
  const seq = ++statsReqSeq
  endpointStatsLoading.value = true
  try {
    const stats = await usageAPI.getStats(normalizedFilters.value)
    if (seq !== statsReqSeq) return
    usageStats.value = stats
    inboundEndpointStats.value = stats.endpoints || []
    upstreamEndpointStats.value = []
    endpointPathStats.value = []
  } catch (error) {
    if (seq !== statsReqSeq) return
    console.error('Failed to load usage stats:', error)
    inboundEndpointStats.value = []
    upstreamEndpointStats.value = []
    endpointPathStats.value = []
  } finally {
    if (seq === statsReqSeq) endpointStatsLoading.value = false
  }
}

const loadModelStats = async () => {
  const seq = ++modelStatsReqSeq
  modelStatsLoading.value = true
  try {
    const response = await usageAPI.getDashboardModels({
      ...normalizedFilters.value,
      model_source: 'requested',
    })
    if (seq !== modelStatsReqSeq) return
    requestedModelStats.value = response.models || []
    refreshModelOptions(response.models || [])
  } catch (error) {
    if (seq !== modelStatsReqSeq) return
    console.error('Failed to load model stats:', error)
    requestedModelStats.value = []
  } finally {
    if (seq === modelStatsReqSeq) modelStatsLoading.value = false
  }
}

const loadChartData = async () => {
  const seq = ++chartReqSeq
  chartsLoading.value = true
  try {
    const snapshot = await usageAPI.getDashboardSnapshotV2({
      ...normalizedFilters.value,
      granularity: granularity.value,
      include_trend: true,
      include_model_stats: false,
      include_group_stats: true,
    })
    if (seq !== chartReqSeq) return
    trendData.value = snapshot.trend || []
    groupStats.value = snapshot.groups || []
  } catch (error) {
    if (seq !== chartReqSeq) return
    console.error('Failed to load chart data:', error)
    trendData.value = []
    groupStats.value = []
  } finally {
    if (seq === chartReqSeq) chartsLoading.value = false
  }
}

const refreshModelOptions = (models: ModelStat[]) => {
  const current = filters.value.model
  const set = new Set(modelOptionValues.value)
  models.forEach((item) => {
    if (item.model) set.add(item.model)
  })
  if (current) set.add(current)
  modelOptionValues.value = Array.from(set).sort()
}

const applyFilters = () => {
  pagination.page = 1
  void loadLogs()
  void loadStats()
  void loadModelStats()
  void loadChartData()
  resetErrorRows()
}

const refreshData = () => {
  void loadLogs()
  void loadStats()
  void loadModelStats()
  void loadChartData()
  if (activeTab.value === 'errors') void loadErrors()
}

const resetFilters = () => {
  const range = getLast24HoursRangeDates()
  startDate.value = range.start
  endDate.value = range.end
  filters.value = {
    start_date: range.start,
    end_date: range.end,
    request_type: undefined,
    native_compaction_v2: null,
    billing_type: null,
    billing_mode: null,
  }
  granularity.value = getGranularityForRange(range.start, range.end)
  applyFilters()
  if (activeTab.value === 'errors') {
    errorFilter.value = { model: '', category: '', api_key_id: null, status_code: null }
    applyErrorFilters()
  }
}

const onDateRangeChange = (range: { startDate: string; endDate: string; preset: string | null }) => {
  startDate.value = range.startDate
  endDate.value = range.endDate
  filters.value.start_date = range.startDate
  filters.value.end_date = range.endDate
  granularity.value = getGranularityForRange(range.startDate, range.endDate)
  applyFilters()
}

const handlePageChange = (page: number) => {
  pagination.page = page
  void loadLogs()
}

const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize
  pagination.page = 1
  void loadLogs()
}

const handleSort = (key: string, order: 'asc' | 'desc') => {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  void loadLogs()
}

const handleIpGeoBatchFailed = () => {
  appStore.showError(t('usage.ipGeo.batchFailed'))
}

const exportPanel = ref<InstanceType<typeof UsageExportTasks> | null>(null)
const exportToCSV = async () => {
  await exportPanel.value?.create({ ...buildUsageListParams(1, 100) })
}

const ALWAYS_VISIBLE = ['created_at']
const DEFAULT_HIDDEN_COLUMNS = ['user_agent']
// Below 768px (mobile list plan): API key (frozen), model, tokens, cost, latency and time; the rest in the settings.
const NARROW_DEFAULT_HIDDEN_COLUMNS = ['reasoning_effort', 'endpoint', 'ip_address', 'group', 'stream', 'billing_mode', 'user_agent']
const HIDDEN_COLUMNS_KEY = 'user-usage-hidden-columns'

const allColumns = computed<Column[]>(() => [
  { key: 'api_key', label: t('usage.apiKeyFilter'), sortable: false },
  { key: 'model', label: t('usage.model'), sortable: true },
  { key: 'reasoning_effort', label: t('usage.reasoningEffort'), sortable: false },
  { key: 'endpoint', label: t('usage.endpoint'), sortable: false },
  { key: 'ip_address', label: 'IP', sortable: false },
  { key: 'group', label: t('admin.usage.group'), sortable: false },
  { key: 'stream', label: t('usage.type'), sortable: false },
  { key: 'billing_mode', label: t('admin.usage.billingMode'), sortable: false },
  { key: 'tokens', label: t('usage.tokens'), sortable: false, class: 'text-right' },
  { key: 'cost', label: t('usage.cost'), sortable: false, class: 'text-right' },
  { key: 'latency', label: t('usage.latency'), sortable: false, class: 'text-right' },
  { key: 'created_at', label: t('usage.time'), sortable: true },
  { key: 'user_agent', label: t('usage.userAgent'), sortable: false },
])

const toggleableColumns = computed(() => allColumns.value.filter((col) => !ALWAYS_VISIBLE.includes(col.key)))
const usageColumns = useColumnVisibility({
  keys: computed(() => toggleableColumns.value.map((col) => col.key)),
  wide: { storageKey: HIDDEN_COLUMNS_KEY, defaultHidden: DEFAULT_HIDDEN_COLUMNS },
  narrow: { storageKey: `${HIDDEN_COLUMNS_KEY}-narrow`, defaultHidden: NARROW_DEFAULT_HIDDEN_COLUMNS },
})
const visibleColumns = computed(() =>
  allColumns.value.filter((col) => ALWAYS_VISIBLE.includes(col.key) || usageColumns.isVisible(col.key))
)

// 错误请求 tab 独立列设置(机制同用量列设置,存储互不影响)
const ERR_ALWAYS_VISIBLE = ['status', 'created_at']
const ERR_DEFAULT_HIDDEN_COLUMNS = ['user_agent']
const ERR_HIDDEN_COLUMNS_KEY = 'user-usage-error-hidden-columns'

// key 须与 UserErrorRequestsTable 的 allColumns 一致
const errAllColumns = computed<Column[]>(() => [
  { key: 'key_name', label: t('usage.errors.keyName') },
  { key: 'model', label: t('usage.errors.model') },
  { key: 'endpoint', label: t('usage.errors.endpoint') },
  { key: 'client_ip', label: 'IP' },
  { key: 'group', label: t('admin.usage.group') },
  { key: 'type', label: t('usage.type') },
  { key: 'platform', label: t('usage.errors.platform') },
  { key: 'category', label: t('usage.errors.category') },
  { key: 'status', label: t('usage.errors.status') },
  { key: 'message', label: t('usage.errors.message') },
  { key: 'created_at', label: t('usage.errors.time') },
  { key: 'user_agent', label: t('usage.userAgent') },
])

const errToggleableColumns = computed(() =>
  errAllColumns.value.filter((col) => !ERR_ALWAYS_VISIBLE.includes(col.key))
)
// The errors tab keeps its own key and its columns on phones too (it switches to the table as it is).
const errorColumns = useColumnVisibility({
  keys: computed(() => errToggleableColumns.value.map((col) => col.key)),
  wide: { storageKey: ERR_HIDDEN_COLUMNS_KEY, defaultHidden: ERR_DEFAULT_HIDDEN_COLUMNS },
  narrow: { storageKey: `${ERR_HIDDEN_COLUMNS_KEY}-narrow`, defaultHidden: ERR_DEFAULT_HIDDEN_COLUMNS },
})
const errVisibleColumnKeys = computed(() =>
  errAllColumns.value
    .filter((col) => ERR_ALWAYS_VISIBLE.includes(col.key) || errorColumns.isVisible(col.key))
    .map((col) => col.key)
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
/** The list's sort when its column is hidden (only the model column can be): named in the phone toolbar. */
const hiddenSort = computed(() => {
  const column = allColumns.value.find((col) => col.key === sortState.sort_by)
  if (!column || visibleColumns.value.some((col) => col.key === column.key)) return null
  return { label: column.label, order: sortState.sort_order as 'asc' | 'desc' }
})

// ---- Phone toolbar (mobile list plan): the fields in a bottom sheet, the chosen ones as tags ----
const [DefineFields, ReuseFields] = createReusableTemplate()
const phone = usePhoneViewport()
const moreOpen = ref(false)
watch(phone, (on) => { if (!on) moreOpen.value = false })

const chosen = (value: unknown) => value !== null && value !== undefined && value !== ''
const optionLabel = (options: SelectOption[], value: unknown) =>
  options.find((option) => option.value === value)?.label ?? String(value)

/** The current tab's filters that are not "all" ("Filters n" counts them); the date range sits above, outside. */
const filterTags = computed<UsageFilterTag[]>(() => {
  const out: UsageFilterTag[] = []
  const add = (key: string, label: string, text: string) => out.push({ key, label, text })
  if (activeTab.value === 'errors') {
    const f = errorFilter.value
    if (chosen(f.api_key_id)) add('api_key_id', t('usage.errors.keyName'), optionLabel(errorKeyOptions.value, f.api_key_id))
    if (chosen(f.model)) add('model', t('usage.errors.model'), String(f.model))
    if (chosen(f.category)) add('category', t('usage.errors.category'), optionLabel(errorCategoryOptions.value, f.category))
    if (chosen(f.status_code)) add('status_code', t('usage.errors.status'), optionLabel(errorStatusOptions.value, f.status_code))
    return out
  }
  const f = filters.value
  if (chosen(f.api_key_id)) add('api_key_id', t('usage.apiKeyFilter'), optionLabel(apiKeyOptions.value, f.api_key_id))
  if (chosen(f.model)) add('model', t('usage.model'), String(f.model))
  if (chosen(f.group_id)) add('group_id', t('admin.usage.group'), optionLabel(groupOptions.value, f.group_id))
  if (chosen(f.request_type)) add('request_type', t('usage.type'), optionLabel(requestTypeOptions.value, f.request_type))
  if (chosen(f.native_compaction_v2)) add('native_compaction_v2', t('usage.compactionFilter'), optionLabel(compactionOptions.value, f.native_compaction_v2))
  if (subscriptionFeatureEnabled.value && chosen(f.billing_type)) add('billing_type', t('admin.usage.billingType'), optionLabel(billingTypeOptions.value, f.billing_type))
  if (chosen(f.billing_mode)) add('billing_mode', t('admin.usage.billingMode'), optionLabel(billingModeOptions.value, f.billing_mode))
  return out
})

/** Each field's "all" value, as the page's reset sets it. */
const ERROR_FILTER_DEFAULTS = { api_key_id: null, model: '', category: '', status_code: null } as const
const USAGE_FILTER_DEFAULTS: Record<string, null | undefined> = {
  api_key_id: undefined, model: undefined, group_id: undefined, request_type: undefined, native_compaction_v2: null,
  billing_type: null, billing_mode: null,
}
const clearFilterTag = (key: string) => {
  if (activeTab.value === 'errors') {
    const field = key as keyof typeof ERROR_FILTER_DEFAULTS
    errorFilter.value = { ...errorFilter.value, [field]: ERROR_FILTER_DEFAULTS[field] }
  } else {
    filters.value = { ...filters.value, [key]: USAGE_FILTER_DEFAULTS[key] }
  }
}
const applyCurrentFilters = () => (activeTab.value === 'errors' ? applyErrorFilters() : applyFilters())
const removeFilterTag = (key: string) => {
  clearFilterTag(key)
  applyCurrentFilters()
}
/** "Clear all" in the sheet: the tab's chosen filters, applied once; the date range stays. */
const clearAllFilters = () => {
  for (const tag of filterTags.value) clearFilterTag(tag.key)
  applyCurrentFilters()
}

const loadApiKeys = async () => {
  const firstPage = await keysAPI.list(1, 100)
  const keys = [...firstPage.items]
  for (let page = 2; page <= firstPage.pages && keys.length > 0; page++) {
    const response = await keysAPI.list(page, 100)
    if (response.items.length === 0) break
    keys.push(...response.items)
  }
  return keys
}

const loadFilterOptions = async () => {
  try {
    const [keys, availableGroups] = await Promise.all([
      loadApiKeys(),
      userGroupsAPI.getAvailable(),
    ])
    apiKeys.value = keys
    groups.value = availableGroups
  } catch (error) {
    console.error('Failed to load usage filter options:', error)
  }
}

const resetErrorRows = () => {
  errorPage.value = 1
  if (activeTab.value === 'errors') {
    void loadErrors()
  } else {
    errorRows.value = []
    errorTotal.value = 0
  }
}

const loadErrors = async () => {
  errorLoading.value = true
  try {
    const resp = await usageAPI.listMyErrorRequests({
      page: errorPage.value,
      page_size: errorPageSize.value,
      start_date: startDate.value,
      end_date: endDate.value,
      model: (errorFilter.value.model ?? '').trim() || undefined,
      category: errorFilter.value.category || undefined,
      api_key_id: errorFilter.value.api_key_id ?? undefined,
      status_code: errorFilter.value.status_code ?? undefined,
      sort_by: errorSortBy.value,
      sort_order: errorSortOrder.value,
    })
    errorRows.value = resp.items
    errorTotal.value = resp.total
  } catch (error) {
    console.error('[UsageView] loadErrors failed:', error)
    appStore.showError(t('usage.errors.failedToLoad'))
  } finally {
    errorLoading.value = false
  }
}

const onErrorSort = (sortBy: string, sortOrder: 'asc' | 'desc') => {
  errorSortBy.value = sortBy
  errorSortOrder.value = sortOrder
  errorPage.value = 1
  void loadErrors()
}

const onErrorPage = (page: number) => {
  errorPage.value = page
  void loadErrors()
}

const onErrorPageSize = (pageSize: number) => {
  errorPageSize.value = pageSize
  errorPage.value = 1
  void loadErrors()
}

const switchToErrors = () => {
  activeTab.value = 'errors'
  if (errorRows.value.length === 0) void loadErrors()
}

onMounted(() => {
  void loadFilterOptions()
  refreshData()
})

onUnmounted(() => {
  abortController?.abort()
})

watch(endpointDistributionSource, () => {
  // Endpoint source switching is handled by the chart component using already loaded stats.
})
</script>
