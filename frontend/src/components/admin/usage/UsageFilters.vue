<template>
  <div :class="flat ? 'p-4 sm:p-6' : 'card p-6'">
    <!-- The fields, defined once: in the row from 1024px, in the phone toolbar's bottom sheet below it. -->
    <DefineFields>
      <!-- User Search -->
      <div ref="userSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[240px]">
        <label class="input-label">{{ t('admin.usage.userFilter') }}</label>
        <input
          v-model="userKeyword"
          type="text"
          class="input pr-8 [@media(pointer:coarse)]:text-base"
          :placeholder="t('admin.usage.searchUserPlaceholder')"
          @input="debounceUserSearch"
          @focus="showUserDropdown = true"
        />
        <button
          v-if="filters.user_id"
          type="button"
          @click="clearUser"
          class="absolute right-2 top-9 text-gray-400"
          aria-label="Clear user filter"
        >
          ✕
        </button>
        <div
          v-if="showUserDropdown && (userResults.length > 0 || userKeyword)"
          class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
        >
          <button
            v-for="u in userResults"
            :key="u.id"
            type="button"
            @click="selectUser(u)"
            class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
          >
            <span>{{ u.email }}<span v-if="u.deleted" class="ml-1 text-xs text-gray-400">（{{ t('admin.usage.userDeletedBadge') }}）</span></span>
            <span class="ml-2 text-xs text-gray-400">#{{ u.id }}</span>
          </button>
        </div>
      </div>

      <!-- API Key Search -->
      <div ref="apiKeySearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[240px]">
        <label class="input-label">{{ t('usage.apiKeyFilter') }}</label>
        <input
          v-model="apiKeyKeyword"
          type="text"
          class="input pr-8 [@media(pointer:coarse)]:text-base"
          :placeholder="t('admin.usage.searchApiKeyPlaceholder')"
          @input="debounceApiKeySearch"
          @focus="onApiKeyFocus"
        />
        <button
          v-if="filters.api_key_id"
          type="button"
          @click="onClearApiKey"
          class="absolute right-2 top-9 text-gray-400"
          aria-label="Clear API key filter"
        >
          ✕
        </button>
        <div
          v-if="showApiKeyDropdown && apiKeyResults.length > 0"
          class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
        >
          <button
            v-for="k in apiKeyResults"
            :key="k.id"
            type="button"
            @click="selectApiKey(k)"
            class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
          >
            <span class="truncate">{{ k.name || `#${k.id}` }}</span>
            <span class="ml-2 text-xs text-gray-400">#{{ k.id }}</span>
          </button>
        </div>
      </div>

      <!-- Model Filter -->
      <div class="w-full sm:w-auto sm:min-w-[220px]">
        <label class="input-label">{{ t('usage.model') }}</label>
        <Select v-model="filters.model" :options="modelOptions" searchable @change="emitChange" />
      </div>

      <!-- Account Filter -->
      <div ref="accountSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[220px]">
        <label class="input-label">{{ t('admin.usage.account') }}</label>
        <input
          v-model="accountKeyword"
          type="text"
          class="input pr-8 [@media(pointer:coarse)]:text-base"
          :placeholder="t('admin.usage.searchAccountPlaceholder')"
          @input="debounceAccountSearch"
          @focus="showAccountDropdown = true"
        />
        <button
          v-if="filters.account_id"
          type="button"
          @click="clearAccount"
          class="absolute right-2 top-9 text-gray-400"
          aria-label="Clear account filter"
        >
          ✕
        </button>
        <div
          v-if="showAccountDropdown && (accountResults.length > 0 || accountKeyword)"
          class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
        >
          <button
            v-for="a in accountResults"
            :key="a.id"
            type="button"
            @click="selectAccount(a)"
            class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
          >
            <span class="truncate">{{ a.name }}</span>
            <span class="ml-2 text-xs text-gray-400">#{{ a.id }}</span>
          </button>
        </div>
      </div>

      <!-- Request Type Filter (usage only) -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('usage.type') }}</label>
        <Select v-model="filters.request_type" :options="requestTypeOptions" @change="emitChange" />
      </div>

      <!-- Native compaction is independent of the transport request type. -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('usage.compactionFilter') }}</label>
        <Select v-model="filters.native_compaction_v2" :options="compactionOptions" @change="emitChange" />
      </div>

      <!-- Billing Type Filter (usage only) -->
      <div v-if="mode !== 'errors'" class="w-full sm:w-auto sm:min-w-[200px]">
        <label class="input-label">{{ t('admin.usage.billingType') }}</label>
        <Select v-model="filters.billing_type" :options="billingTypeOptions" @change="emitChange" />
      </div>

      <!-- Billing Mode Filter (usage only；用户排行的 user-breakdown 接口不支持该维度) -->
      <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[200px]">
        <label class="input-label">{{ t('admin.usage.billingMode') }}</label>
        <Select v-model="filters.billing_mode" :options="billingModeOptions" @change="emitChange" />
      </div>

      <!-- Request result filter (usage only; backend 0.6.5 request ledger: list and stats) -->
      <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('admin.usage.result') }}</label>
        <Select v-model="filters.result" :options="resultOptions" @change="emitChange" />
      </div>

      <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[220px]">
        <label class="input-label">{{ t('admin.usage.upstreamModelAudit') }}</label>
        <Select v-model="filters.upstream_model_mismatch" :options="upstreamModelMismatchOptions" @change="emitChange" />
      </div>

      <!-- Error Phase Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('admin.ops.errorLog.type') }}</label>
        <Select v-model="filters.error_phase" :options="errorPhaseOptions" @change="emitChange" />
      </div>

      <!-- Error Category Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('usage.errors.category') }}</label>
        <Select v-model="filters.error_category" :options="errorCategoryOptions" @change="emitChange" />
      </div>

      <!-- Status Code Filter (errors only) -->
      <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
        <label class="input-label">{{ t('admin.ops.errorLog.status') }}</label>
        <Select v-model="filters.status_code" :options="statusCodeOptions" @change="emitChange" />
      </div>

      <!-- Group Filter -->
      <div class="w-full sm:w-auto sm:min-w-[200px]">
        <label class="input-label">{{ t('admin.usage.group') }}</label>
        <Select v-model="filters.group_id" :options="groupOptions" searchable @change="emitChange" />
      </div>

    </DefineFields>

    <!-- Below 1024px (mobile list plan; the fields are a long form, four rows inline on a tablet): "Filters n" opens
         them in a bottom sheet (applied at once), the chosen ones as removable tags; the count, live state, "Filters"
         (no search here, so it joins this row), refresh, columns and "more" (reset, cleanup, export) on one row. -->
    <TableToolbar v-if="sheet" filter-sheet sheet-below="desktop" :filter-count="tags.length" data-testid="usage-filters-phone" @clear-filters="clearAll">
      <template #sheet>
        <div class="grid gap-4"><ReuseFields /></div>
      </template>
      <template #tags>
        <UsageFilterTags :tags="tags" @remove="removeFilter" />
      </template>
      <template #info><slot name="info" /></template>
      <template v-if="$slots.status" #status><slot name="status" /></template>
      <template #actions>
        <button
          v-if="showActions"
          type="button"
          class="table-toolbar__icon-btn table-toolbar__icon"
          :aria-label="t('common.refresh')"
          data-testid="usage-refresh"
          @click="$emit('refresh')"
        >
          <Icon name="refresh" size="sm" />
        </button>
        <slot name="phone-tools" />
        <button
          v-if="showActions"
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
    <BottomSheet v-if="sheet && showActions" :show="moreOpen" :title="t('common.more')" test-id="usage-more-sheet" @close="moreOpen = false">
      <div class="grid gap-3">
        <button type="button" class="btn btn-secondary w-full" @click="moreOpen = false; $emit('reset')">{{ t('common.reset') }}</button>
        <template v-if="mode === 'usage'">
          <button v-if="canCleanup" type="button" class="btn btn-danger w-full" @click="moreOpen = false; $emit('cleanup')">
            {{ t('admin.usage.cleanup.button') }}
          </button>
          <button v-if="canExport" type="button" class="btn btn-primary w-full" :disabled="exporting" @click="moreOpen = false; $emit('export')">
            {{ t('usage.exportExcel') }}
          </button>
        </template>
      </div>
    </BottomSheet>

    <!-- Toolbar: left filters (multi-line) + right actions -->
    <div v-if="!sheet" class="flex flex-wrap items-end justify-between gap-4">
      <!-- Left: filters (allowed to wrap to multiple rows); below 1024px they take the whole row and the actions wrap under them -->
      <div class="flex flex-1 flex-wrap items-end gap-4 max-lg:basis-full">
        <ReuseFields />
      </div>

      <!-- Right: actions -->
      <div v-if="showActions" class="flex w-full flex-wrap items-center justify-end gap-3 sm:ml-auto sm:w-auto">
        <!-- The sort while its column is hidden, then the page's live indicator, next to the refresh button -->
        <slot name="sort-hint" />
        <slot name="status" />
        <button type="button" @click="$emit('refresh')" class="btn btn-secondary">
          {{ t('common.refresh') }}
        </button>
        <button type="button" @click="$emit('reset')" class="btn btn-secondary">
          {{ t('common.reset') }}
        </button>
        <slot name="after-reset" />
        <template v-if="mode === 'usage'">
          <button v-if="canCleanup" type="button" @click="$emit('cleanup')" class="btn btn-danger">
            {{ t('admin.usage.cleanup.button') }}
          </button>
          <button v-if="canExport" type="button" @click="$emit('export')" :disabled="exporting" class="btn btn-primary">
            {{ t('usage.exportExcel') }}
          </button>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, toRef, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { createReusableTemplate } from '@vueuse/core'
import TableToolbar from '@/components/layout/TableToolbar.vue'
import BottomSheet from '@/components/common/BottomSheet.vue'
import Icon from '@/components/icons/Icon.vue'
import UsageFilterTags, { type UsageFilterTag } from './UsageFilterTags.vue'
import { useTableViewport } from '@/composables/useTableViewport'
import { adminAPI } from '@/api/admin'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import { COMMON_ERROR_STATUS_CODES } from '@/utils/errorBadges'
import type { SimpleApiKey, SimpleUser } from '@/api/admin/usage'

type ModelValue = Record<string, any>

interface Props {
  modelValue: ModelValue
  exporting: boolean
  startDate: string
  endDate: string
  showActions?: boolean
  modelOptions?: string[]
  /**
   * errors 模式:隐藏用量专属字段/按钮,显示错误类型+状态码(错误请求 tab 用)
   * ranking 模式:同 usage 但隐藏计费模式筛选与清理/导出按钮(用户排行 tab 用)
   */
  mode?: 'usage' | 'errors' | 'ranking'
  /** 嵌入统一卡片内使用：去掉自身卡片外观 */
  flat?: boolean
  /** 清理使用记录（超级管理员） */
  canCleanup?: boolean
  /** 导出使用记录（普通管理员需要“导出业务数据”权限） */
  canExport?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  showActions: true,
  mode: 'usage',
  flat: false,
  canCleanup: true,
  canExport: true
})
const emit = defineEmits([
  'update:modelValue',
  'change',
  'refresh',
  'reset',
  'export',
  'cleanup'
])

const { t } = useI18n()
const filters = toRef(props, 'modelValue')

const userSearchRef = ref<HTMLElement | null>(null)
const apiKeySearchRef = ref<HTMLElement | null>(null)
const accountSearchRef = ref<HTMLElement | null>(null)

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
/**
 * The names of the chosen user, API key and account, for the phone tags: what is filtered on, never the search box's
 * draft (typing in a box after choosing changes nothing until another result is picked).
 */
const chosenUser = ref('')
const chosenApiKey = ref('')
const chosenAccount = ref('')
const showUserDropdown = ref(false)
let userSearchTimeout: ReturnType<typeof setTimeout> | null = null
let userSearchSequence = 0

const apiKeyKeyword = ref('')
const apiKeyResults = ref<SimpleApiKey[]>([])
const showApiKeyDropdown = ref(false)
let apiKeySearchTimeout: ReturnType<typeof setTimeout> | null = null

interface SimpleAccount {
  id: number
  name: string
}
const accountKeyword = ref('')
const accountResults = ref<SimpleAccount[]>([])
const showAccountDropdown = ref(false)
let accountSearchTimeout: ReturnType<typeof setTimeout> | null = null

const modelOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allModels') },
  ...(props.modelOptions ?? []).map((m) => ({ value: m, label: m })),
])
const groupOptions = ref<SelectOption[]>([{ value: null, label: t('admin.usage.allGroups') }])

const requestTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'live', label: t('usage.live') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'sync', label: t('usage.sync') },
  { value: 'cyber', label: t('usage.cyber') }
])

const compactionOptions = ref<SelectOption[]>([
  { value: null, label: t('usage.allCompactionTypes') },
  { value: true, label: t('usage.compactionOnly') }
])

const billingTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingTypes') },
  { value: 0, label: t('admin.usage.billingTypeBalance') },
  { value: 1, label: t('admin.usage.billingTypeSubscription') }
])

// 请求结果（后端 0.6.5 请求层）：1 成功，2 已救回，3 失败，4 已取消
const resultOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allResults') },
  { value: 1, label: t('admin.usage.resultSuccess') },
  { value: 2, label: t('admin.usage.resultRescued') },
  { value: 3, label: t('admin.usage.resultFailed') },
  { value: 4, label: t('admin.usage.resultCanceled') }
])

// 错误类型对应后端 phase 参数(与错误表"类型"徽章同语义)
const errorPhaseOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'upstream', label: t('admin.ops.errorLog.typeUpstream') },
  { value: 'account_auth', label: t('admin.ops.errorLog.typeAccountAuth') },
  { value: 'request', label: t('admin.ops.errorLog.typeRequest') },
  { value: 'auth', label: t('admin.ops.errorLog.typeAuth') },
  { value: 'routing', label: t('admin.ops.errorLog.typeRouting') },
  { value: 'internal', label: t('admin.ops.errorLog.typeInternal') },
])

// 分类码同用户端 /usage 错误筛选;"other" 无法反查为过滤条件,刻意不列
const errorCategoryCodes = ['auth', 'rate_limit', 'quota', 'invalid_request', 'service_unavailable', 'upstream', 'internal', 'cyber']

const errorCategoryOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allCategories') },
  ...errorCategoryCodes.map((c) => ({ value: c, label: t('usage.errors.categories.' + c) })),
])

const statusCodeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allStatuses') },
  ...COMMON_ERROR_STATUS_CODES.map((c) => ({ value: c, label: String(c) })),
])

const billingModeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingModes') },
  { value: 'token', label: t('admin.usage.billingModeToken') },
  { value: 'per_request', label: t('admin.usage.billingModePerRequest') },
  { value: 'image', label: t('admin.usage.billingModeImage') },
  { value: 'video', label: t('admin.usage.billingModeVideo') }
])

const upstreamModelMismatchOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allUpstreamModelAudit') },
  { value: true, label: t('admin.usage.upstreamModelMismatchOnly') },
  { value: false, label: t('admin.usage.upstreamModelMatchedOnly') }
])

const emitChange = () => emit('change')

const [DefineFields, ReuseFields] = createReusableTemplate()
/** Below 1024px the toolbar with the bottom sheet (TableToolbar's sheetBelow 'desktop'); from 1024px the row. */
const { desktop } = useTableViewport()
const sheet = computed(() => !desktop.value)
const moreOpen = ref(false)
watch(sheet, (on) => { if (!on) moreOpen.value = false })

/** A select's label for the chosen value; nothing for "all" (null, undefined or an empty string). */
const chosen = (value: unknown) => value !== null && value !== undefined && value !== ''
const optionLabel = (options: SelectOption[], value: unknown) =>
  options.find((option) => option.value === value)?.label ?? String(value)

/**
 * The filters in this mode that are not "all", in the fields' order, as phone tags ("Filters n" counts them). The date
 * range is not one of them: it sits above the list, outside the sheet.
 */
const tags = computed<UsageFilterTag[]>(() => {
  const f = filters.value
  const out: UsageFilterTag[] = []
  const add = (key: string, label: string, text: string) => out.push({ key, label, text })
  if (chosen(f.user_id)) add('user', t('admin.usage.userFilter'), chosenUser.value || `#${f.user_id}`)
  if (chosen(f.api_key_id)) add('api_key', t('usage.apiKeyFilter'), chosenApiKey.value || `#${f.api_key_id}`)
  if (chosen(f.model)) add('model', t('usage.model'), String(f.model))
  if (chosen(f.account_id)) add('account', t('admin.usage.account'), chosenAccount.value || `#${f.account_id}`)
  if (props.mode !== 'errors') {
    if (chosen(f.request_type)) add('request_type', t('usage.type'), optionLabel(requestTypeOptions.value, f.request_type))
    if (chosen(f.native_compaction_v2)) add('native_compaction_v2', t('usage.compactionFilter'), optionLabel(compactionOptions.value, f.native_compaction_v2))
    if (chosen(f.billing_type)) add('billing_type', t('admin.usage.billingType'), optionLabel(billingTypeOptions.value, f.billing_type))
  }
  if (props.mode === 'usage') {
    if (chosen(f.billing_mode)) add('billing_mode', t('admin.usage.billingMode'), optionLabel(billingModeOptions.value, f.billing_mode))
    if (chosen(f.result)) add('result', t('admin.usage.result'), optionLabel(resultOptions.value, f.result))
    if (chosen(f.upstream_model_mismatch)) add('upstream_model_mismatch', t('admin.usage.upstreamModelAudit'), optionLabel(upstreamModelMismatchOptions.value, f.upstream_model_mismatch))
  }
  if (props.mode === 'errors') {
    if (chosen(f.error_phase)) add('error_phase', t('admin.ops.errorLog.type'), optionLabel(errorPhaseOptions.value, f.error_phase))
    if (chosen(f.error_category)) add('error_category', t('usage.errors.category'), optionLabel(errorCategoryOptions.value, f.error_category))
    if (chosen(f.status_code)) add('status_code', t('admin.ops.errorLog.status'), optionLabel(statusCodeOptions.value, f.status_code))
  }
  if (chosen(f.group_id)) add('group', t('admin.usage.group'), optionLabel(groupOptions.value, f.group_id))
  return out
})

/** Each select's "all" value, as the page's reset sets it. */
const SELECT_DEFAULTS: Record<string, null | undefined> = {
  model: undefined, request_type: undefined, native_compaction_v2: null, billing_type: null, billing_mode: undefined,
  result: null, upstream_model_mismatch: undefined, error_phase: undefined, error_category: undefined,
  status_code: undefined, group_id: undefined,
}
const FIELD_OF_TAG: Record<string, string> = { group: 'group_id' }

const clearTag = (key: string) => {
  if (key === 'user') { clearPendingUserSearch(); userKeyword.value = ''; userResults.value = []; filters.value.user_id = undefined; clearApiKey(); return }
  if (key === 'api_key') { clearApiKey(); return }
  if (key === 'account') { accountKeyword.value = ''; accountResults.value = []; filters.value.account_id = undefined; return }
  const field = FIELD_OF_TAG[key] ?? key
  filters.value[field] = SELECT_DEFAULTS[field]
}
const removeFilter = (key: string) => {
  clearTag(key)
  emitChange()
}
/** "Clear all" in the sheet: every chosen filter, applied once; the date range stays. */
const clearAll = () => {
  for (const tag of tags.value) clearTag(tag.key)
  emitChange()
}

const clearPendingUserSearch = () => {
  if (userSearchTimeout) {
    clearTimeout(userSearchTimeout)
    userSearchTimeout = null
  }
  userSearchSequence += 1
}

const debounceUserSearch = () => {
  clearPendingUserSearch()
  const query = userKeyword.value.trim()
  if (!query) {
    userResults.value = []
    return
  }

  const sequence = userSearchSequence
  userSearchTimeout = setTimeout(async () => {
    userSearchTimeout = null
    try {
      const results = await adminAPI.usage.searchUsers(query)
      if (sequence === userSearchSequence) {
        userResults.value = results.sort((a, b) => Number(a.deleted) - Number(b.deleted))
      }
    } catch {
      if (sequence === userSearchSequence) {
        userResults.value = []
      }
    }
  }, 300)
}

const debounceApiKeySearch = () => {
  if (apiKeySearchTimeout) clearTimeout(apiKeySearchTimeout)
  apiKeySearchTimeout = setTimeout(async () => {
    try {
      apiKeyResults.value = await adminAPI.usage.searchApiKeys(
        filters.value.user_id,
        apiKeyKeyword.value || ''
      )
    } catch {
      apiKeyResults.value = []
    }
  }, 300)
}

const selectUser = async (u: SimpleUser) => {
  clearPendingUserSearch()
  userKeyword.value = u.email
  showUserDropdown.value = false
  filters.value.user_id = u.id
  chosenUser.value = u.email
  clearApiKey()

  // Auto-load API keys for this user
  try {
    apiKeyResults.value = await adminAPI.usage.searchApiKeys(u.id, '')
  } catch {
    apiKeyResults.value = []
  }

  emitChange()
}

const clearUser = () => {
  clearPendingUserSearch()
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  filters.value.user_id = undefined
  clearApiKey()
  emitChange()
}

const selectApiKey = (k: SimpleApiKey) => {
  apiKeyKeyword.value = k.name || String(k.id)
  showApiKeyDropdown.value = false
  filters.value.api_key_id = k.id
  chosenApiKey.value = k.name || `#${k.id}`
  emitChange()
}

const clearApiKey = () => {
  apiKeyKeyword.value = ''
  apiKeyResults.value = []
  showApiKeyDropdown.value = false
  filters.value.api_key_id = undefined
}

const onClearApiKey = () => {
  clearApiKey()
  emitChange()
}

const debounceAccountSearch = () => {
  if (accountSearchTimeout) clearTimeout(accountSearchTimeout)
  accountSearchTimeout = setTimeout(async () => {
    if (!accountKeyword.value) {
      accountResults.value = []
      return
    }
    try {
      const res = await adminAPI.accounts.list(1, 20, { search: accountKeyword.value })
      accountResults.value = res.items.map((a) => ({ id: a.id, name: a.name }))
    } catch {
      accountResults.value = []
    }
  }, 300)
}

const selectAccount = (a: SimpleAccount) => {
  accountKeyword.value = a.name
  showAccountDropdown.value = false
  filters.value.account_id = a.id
  chosenAccount.value = a.name
  emitChange()
}

const clearAccount = () => {
  accountKeyword.value = ''
  accountResults.value = []
  showAccountDropdown.value = false
  filters.value.account_id = undefined
  emitChange()
}

const onApiKeyFocus = () => {
  showApiKeyDropdown.value = true
  // Trigger search if no results yet
  if (apiKeyResults.value.length === 0) {
    debounceApiKeySearch()
  }
}

const onDocumentClick = (e: MouseEvent) => {
  const target = e.target as Node | null
  if (!target) return

  const clickedInsideUser = userSearchRef.value?.contains(target) ?? false
  const clickedInsideApiKey = apiKeySearchRef.value?.contains(target) ?? false
  const clickedInsideAccount = accountSearchRef.value?.contains(target) ?? false

  if (!clickedInsideUser) showUserDropdown.value = false
  if (!clickedInsideApiKey) showApiKeyDropdown.value = false
  if (!clickedInsideAccount) showAccountDropdown.value = false
}

watch(
  () => props.startDate,
  (value) => {
    filters.value.start_date = value
  },
  { immediate: true }
)

watch(
  () => props.endDate,
  (value) => {
    filters.value.end_date = value
  },
  { immediate: true }
)

watch(
  () => filters.value.user_id,
  (userId) => {
    if (!userId) {
      clearPendingUserSearch()
      userKeyword.value = ''
      userResults.value = []
      chosenUser.value = ''
    }
  }
)

watch(
  () => filters.value.api_key_id,
  (apiKeyId) => {
    if (!apiKeyId) {
      apiKeyKeyword.value = ''
      apiKeyResults.value = []
      chosenApiKey.value = ''
    }
  }
)

watch(
  () => filters.value.account_id,
  (accountId) => {
    if (!accountId) {
      accountKeyword.value = ''
      accountResults.value = []
      chosenAccount.value = ''
    }
  }
)

onMounted(async () => {
  document.addEventListener('click', onDocumentClick)
  try {
    const gs = await adminAPI.groups.list(1, 1000)
    groupOptions.value.push(...gs.items.map((g: any) => ({ value: g.id, label: g.name })))
  } catch {
    // Ignore filter option loading errors (page still usable)
  }
})

onUnmounted(() => {
  clearPendingUserSearch()
  document.removeEventListener('click', onDocumentClick)
})

// 供外部(如用户排行下钻)在程序化设置 user_id 后回显选中的用户邮箱
const setUserKeyword = (email: string) => {
  clearPendingUserSearch()
  userKeyword.value = email
  chosenUser.value = email
  userResults.value = []
  showUserDropdown.value = false
}

const getUserSearchRevision = () => userSearchSequence

// 供外部（如带 account_id 的链接跳转）在程序化设置 account_id 后回显账号名
const setAccountKeyword = (name: string) => {
  if (accountSearchTimeout) clearTimeout(accountSearchTimeout)
  accountKeyword.value = name
  chosenAccount.value = name
  accountResults.value = []
  showAccountDropdown.value = false
}

defineExpose({ getUserSearchRevision, setUserKeyword, setAccountKeyword })
</script>
