<template>
  <AppLayout>
    <TablePageLayout class="accounts-layout">
      <template #filters>
        <AccountsToolbar
          ref="toolbarRef"
          :filters="filters"
          :search="search"
          :groups="groups"
          :live-status="liveStatus"
          :pause="table.pause"
          :resume="table.resume"
          :refreshing="loading"
          :can-create="canManage"
          @update:search="onSearchInput"
          @change="applyFilters"
          @refresh="handleManualRefresh"
          @create="showCreate = true"
        >
          <template #more-actions>
            <AccountToolsMenu
              :can-manage="canManage"
              :is-super-admin="isSuperAdmin"
              :is-admin="authStore.isAdmin"
              :selected-count="selIds.length"
              :columns="orderedToggleableColumns"
              :is-visible="isColumnVisible"
              :customized="!columnsAtDefault"
              @action="handleToolAction"
              @toggle-column="toggleColumn"
              @reorder-columns="setColumnOrder"
              @reset-columns="resetColumns"
            />
          </template>
        </AccountsToolbar>
      </template>
      <template #table>
        <AccountBulkActionsBar
          v-if="canManage && selIds.length > 0"
          :selected-ids="selIds"
          :total-results="total"
          :selecting-all="selectingAllResults"
          :all-results-selected="allResultsSelected"
          :iq-busy="runningIQBatch"
          :scheduling-busy="selIds.some((id) => togglingSchedulable.has(id))"
          :sched-busy="sqBatchBusy"
          :recover-busy="recoveryBatchRunning || selIds.some(isRecovering)"
          @delete="handleBulkDelete"
          @reset-status="handleBulkResetStatus"
          @refresh-token="handleBulkRefreshToken"
          @probe-upstream-billing="handleBulkProbeUpstreamBilling"
          @run-iq-check="handleBulkRunIQCheck"
          @edit-selected="openBulkEditSelected"
          @clear="clearSelection"
          @select-page="selectCurrentPage"
          @select-all-results="handleSelectAllResults"
          @toggle-schedulable="handleBulkToggleSchedulable"
          @sched-action="openSchedBatch"
        />
        <div ref="accountTableRef" class="account-table relative flex min-h-0 flex-1 flex-col overflow-hidden" :class="{ 'is-roomy': columnLayout.roomy }" :style="columnLayout.vars">
          <PendingChangesPill :count="pending" :label="changesText" :tip="t('admin.accounts.table.changesTip')" @merge="applyPending" />
          <DataTable
            ref="dataTableRef"
            :columns="cols"
            :data="rows"
            :loading="loading"
            row-key="id"
            :row-class="accountRowClass"
            :server-side-sort="true"
            default-sort-key="last_used_at"
            default-sort-order="desc"
            :sort-storage-key="ACCOUNT_SORT_STORAGE_KEY"
            :estimate-row-height="78"
            :overscan="5"
            :virtualize-threshold="50"
            @sort="handleSort"
          >
            <template #header-select>
              <input type="checkbox" class="acc-check" :checked="allVisibleSelected" :disabled="!canManage" :aria-label="t('common.selectAll')" @click.stop @change="toggleVisible(($event.target as HTMLInputElement).checked)" />
            </template>
            <template #cell-select="{ row }">
              <div class="acell acell--row"><input type="checkbox" class="acc-check" :checked="isSelected(row.id)" :disabled="!canManage" :aria-label="row.name" @click="noteRowCheckShift" @change="handleRowCheck(row.id, $event)" /></div>
            </template>
            <template #cell-name="{ row }">
              <div class="acell"><AccountNameCell :account="row" /></div>
            </template>
            <template #cell-status="{ row }">
              <div class="acell">
                <AccountStatusCell :account="row" @show-temp-unsched="handleShowTempUnsched" />
                <AccountStreamOnlyBadge :account="row" @cleared="mergeAccount($event as Account)" />
              </div>
            </template>
            <template #cell-schedulable="{ row }">
              <div class="acell acell--row">
                <AccountSwitch :checked="row.schedulable" :label="t('admin.accounts.columns.schedulable') + ' · ' + row.name" :disabled="!canManage" :busy="togglingSchedulable.has(row.id)" @toggle="handleToggleSchedulable(row, $event)" />
              </div>
            </template>
            <template #header-groups="{ column }">
              <span class="inline-flex items-center gap-1.5">{{ column.label }}<SchedPauseMark :global-enabled="qualityMeta?.global_enabled" :stale="qualityMeta?.stale" /></span>
            </template>
            <template #cell-groups="{ row }">
              <div class="acell">
                <AccountGroupPills
                  :account="row"
                  :groups="groupsOf(row)"
                  :quality="row.platform === 'openai' ? row.quality ?? null : null"
                  :readonly="!canPlaceGroups"
                  @open-quality="(groupId: number) => openQuality(row, groupId)"
                  @edit-groups="(event: MouseEvent) => openGroupsEditor(row, event)"
                />
              </div>
            </template>
            <template #cell-sq_recent="{ row }">
              <div class="acell"><AccountRecentCell :quality="row.platform === 'openai' ? row.quality ?? null : null" /></div>
            </template>
            <template #cell-iq_check="{ row }">
              <div class="acell"><AccountIQCell :account="row" :disabled="!canManage" :busy="togglingIQ.has(row.id)" @toggle="handleToggleIQCheck(row, $event)" @records="iqRecordsAccount = { ...row }" /></div>
            </template>
            <template #cell-capacity="{ row }">
              <div class="acell"><AccountCapacityCell :account="row" /></div>
            </template>
            <template #header-upstream_billing_rate="{ column }">
              <!-- Short label; the full name and the trust warning are in the tip, no info icon (7e-8). -->
              <span class="acol-head" v-tip="() => `${t('admin.accounts.columns.upstreamBillingRateFull')}\n${t('admin.accounts.upstreamBilling.trustWarning')}`" data-test="upstream-rate-head">{{ column.label }}</span>
            </template>
            <template #cell-upstream_billing_rate="{ row }">
              <div class="acell">
                <UpstreamBillingRateCell :account="row" :global-probe-enabled="upstreamBillingProbeGloballyEnabled" :now="now" :probing="probingUpstreamBilling.has(row.id)" @probe="handleProbeUpstreamBilling(row)" />
              </div>
            </template>
            <template #cell-today_stats="{ row }">
              <div class="acell"><AccountTodayCell :stats="row.today_stats" /></div>
            </template>
            <template #cell-id="{ value }">
              <div class="acell"><span class="font-mono text-xs text-fg-secondary">#{{ value }}</span></div>
            </template>
            <template #cell-platform_type="{ row }">
              <div class="acell">
                <div class="flex flex-wrap items-center gap-1">
                  <PlatformTypeBadge
                    :platform="row.platform"
                    :type="row.type"
                    :auth-mode="getOpenAIAuthMode(row)"
                    :plan-type="getAccountPlanType(row)"
                    :privacy-mode="row.extra?.privacy_mode || row.parent_privacy_mode"
                    :subscription-expires-at="row.credentials?.subscription_expires_at || row.parent_subscription_expires_at"
                  />
                  <span v-if="antigravityTier(row)" :class="['inline-block rounded px-1.5 py-0.5 text-[10px] font-medium', antigravityTier(row)?.className]">{{ antigravityTier(row)?.label }}</span>
                </div>
                <div v-if="openAICompactMeta(row)" :class="['inline-flex items-center gap-1.5 pl-0.5 text-[11px] font-medium leading-4', openAICompactMeta(row)?.className]" :title="openAICompactTitle(row)">
                  <span :class="['h-1.5 w-1.5 rounded-full', openAICompactMeta(row)?.dotClass]" />
                  <span>{{ openAICompactMeta(row)?.label }}</span>
                </div>
              </div>
            </template>
            <template #cell-priority="{ row }">
              <div class="acell acell--row">
                <AccountPriorityCell :account="row" :save="(priority: number) => savePriority(row, priority)" @error="(message: string) => appStore.showError(message)" />
              </div>
            </template>
            <template #cell-usage="{ row }">
              <div class="acell">
                <AccountUsageFit>
                  <AccountUsageCell
                    :account="row"
                    :today-stats="row.today_stats ?? null"
                    :hide-today-stats="isColumnVisible('today_stats')"
                    :batched-usage="row.usage_info ?? null"
                    :batched-usage-error="row.usage_error ?? null"
                    :request-batched-usage="requestUsage"
                    @account-updated="handleAccountUpdated"
                    @usage-loaded="(usage: AccountUsageInfo) => live.noteUsage(row.id, usage)"
                  />
                </AccountUsageFit>
              </div>
            </template>
            <template #cell-proxy="{ row }">
              <div class="acell">
                <div v-if="row.proxy" class="flex items-center gap-2">
                  <span class="text-sm text-fg-primary">{{ row.proxy.name }}</span>
                  <span v-if="row.proxy.country_code" class="text-xs text-fg-secondary">({{ row.proxy.country_code }})</span>
                </div>
                <span v-else class="text-sm text-fg-muted">-</span>
                <div v-if="row.proxy && row.proxy.expires_at" class="flex items-center gap-2 text-xs">
                  <span class="text-fg-secondary">{{ formatDateTime(row.proxy.expires_at) }}</span>
                  <span :class="proxyExpiryBadgeClass(row.proxy.expires_at, row.proxy.status)">{{ proxyExpiryText(row.proxy) }}</span>
                </div>
                <div v-if="row.proxy_fallback_origin_id" class="flex items-center gap-1">
                  <span class="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200" :title="t('admin.accounts.fallbackActiveTip', { origin: row.proxy_fallback_origin_name })">{{ t('admin.accounts.fallbackActive') }}</span>
                  <button v-if="isSuperAdmin" class="rounded border border-line-control px-1.5 py-0.5 text-xs text-fg-secondary hover:bg-surface-control-hover" data-testid="revert-proxy-fallback" @click="onRevertFallback(row)">{{ t('admin.accounts.revertProxy') }}</button>
                </div>
              </div>
            </template>
            <template #cell-rate_multiplier="{ row }">
              <div class="acell acell--row">
                <span class="inline-flex items-center gap-1 text-sm text-fg-primary">
                  <span>{{ formatMultiplier(row.rate_multiplier ?? 1) }}x</span>
                  <span v-if="row.extra?.upstream_billing_rate_sync_enabled === true" class="inline-flex cursor-help text-fg-secondary" :aria-label="t('admin.accounts.upstreamBilling.syncedRateTooltip')" :title="t('admin.accounts.upstreamBilling.syncedRateTooltip')" data-testid="account-rate-sync-indicator"><Icon name="sync" size="xs" /></span>
                </span>
              </div>
            </template>
            <template #cell-upstream_balance="{ row }">
              <div class="acell"><UpstreamBalanceCell :item="row.upstream_balance ?? undefined" :now="now" /></div>
            </template>
            <template #cell-last_used_at="{ value }">
              <div class="acell"><span class="text-sm text-fg-secondary">{{ formatRelativeTime(value) }}</span></div>
            </template>
            <template #cell-created_at="{ value }">
              <div class="acell"><span class="text-sm text-fg-secondary">{{ formatDateTime(value) }}</span></div>
            </template>
            <template #cell-expires_at="{ row, value }">
              <div class="acell">
                <span class="text-sm text-fg-secondary">{{ formatExpiresAt(value) }}</span>
                <div v-if="isExpired(value) || (row.auto_pause_on_expired && value)" class="flex items-center gap-1">
                  <span v-if="isExpired(value)" class="inline-flex items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">{{ t('admin.accounts.expired') }}</span>
                  <span v-if="row.auto_pause_on_expired && value" class="inline-flex items-center rounded-md bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300">{{ t('admin.accounts.autoPauseOnExpired') }}</span>
                </div>
              </div>
            </template>
            <template #cell-notes="{ value }">
              <div class="acell">
                <span v-if="value" :title="value" class="block max-w-xs truncate text-sm text-fg-secondary">{{ value }}</span>
                <span v-else class="text-sm text-fg-muted">-</span>
              </div>
            </template>
            <template #cell-actions="{ row }">
              <div class="acell acell--end">
                <button v-if="canManage" type="button" v-tip="t('common.edit')" :aria-label="`${t('common.edit')} ${row.name}`" class="acc-op sq-focus" data-test="row-edit" @click="handleEdit(row)">
                  <Icon name="edit" size="sm" />
                </button>
                <button type="button" v-tip="t('common.more')" :aria-label="`${t('common.more')} ${row.name}`" aria-haspopup="menu" class="acc-op sq-focus" data-test="row-more" @click="openMenu(row, $event)">
                  <Icon name="more" size="sm" />
                </button>
              </div>
            </template>
          </DataTable>
        </div>
      </template>
      <template #pagination>
        <Pagination v-if="total > 0" class="accounts-pagination" :page="shownPage.page" :total="total" :page-size="shownPage.page_size" @update:page="handlePageChange" @update:pageSize="handlePageSizeChange" />
      </template>
    </TablePageLayout>
    <CreateAccountModal v-if="createModalMounted" :show="showCreate" :proxies="proxies" :groups="groups" @close="showCreate = false" @created="reload" />
    <EditAccountModal v-if="editModalMounted" :show="showEdit" :account="edAcc" :proxies="proxies" :groups="groups" @close="showEdit = false" @updated="handleAccountUpdated" />
    <ReAuthAccountModal v-if="reAuthModalMounted" :show="showReAuth" :account="reAuthAcc" @close="closeReAuthModal" @reauthorized="handleAccountUpdated" />
    <AccountTestModal v-if="testModalMounted" :show="showTest" :account="testingAcc" @close="closeTestModal" />
    <AccountStatsModal v-if="statsModalMounted" :show="showStats" :account="statsAcc" @close="closeStatsModal" />
    <ScheduledTestsPanel v-if="schedulePanelMounted" :show="showSchedulePanel" :account-id="scheduleAcc?.id ?? null" :model-options="scheduleModelOptions" :readonly="!canManage" @close="closeSchedulePanel" />
    <IQCheckResultsModal v-if="iqRecordsAccount" :show="true" :account="iqRecordsAccount" @close="iqRecordsAccount = null" @updated="handleIQSummaryUpdated" />
    <AccountGroupPicker
      v-if="picker.account"
      ref="pickerRef"
      :key="picker.account.id"
      :account="picker.account"
      :groups="groups"
      :anchor="picker.anchor"
      :focus-search="picker.focusSearch"
      :save="saveGroups"
      @close="picker.account = null"
    />
    <AccountActionMenu :show="menu.show" :account="menu.acc" :anchor-rect="menu.anchorRect" :recoverable="menuRecoverable(menu.acc)" :recovering="!!menu.acc && isRecovering(menu.acc.id)" @close="menu.show = false" @test="handleTest" @stats="handleViewStats" @schedule="handleSchedule" @iq-run="handleRunIQCheck" @iq-records="iqRecordsAccount = $event" @duplicate="handleDuplicateAccount" @reauth="handleReAuth" @refresh-token="handleRefresh" @recover-state="recoverOne" @delete="handleDelete" @reset-quota="handleResetQuota" @set-privacy="handleSetPrivacy" @create-spark-shadow="handleCreateSparkShadow" @manage-openai-totp="handleManageOpenAITotp" />
    <OpenAILoginImportDialog v-if="authStore.isAdmin && showOpenAILogin" :show="showOpenAILogin" :proxies="proxies" :groups="groups" @close="showOpenAILogin = false" @imported="handleDataImported" />
    <OpenAITotpDialog v-if="authStore.isAdmin && openAITotpAccount" :show="true" :account="openAITotpAccount" @close="openAITotpAccount = null" />
    <SyncFromCrsModal v-if="syncModalMounted" :show="showSync" @close="showSync = false" @synced="reload" />
    <ImportDataModal v-if="importModalMounted" :show="showImportData" @close="showImportData = false" @imported="handleDataImported" />
    <BulkEditAccountModal
      v-if="bulkEditModalMounted"
      :show="showBulkEdit"
      :account-ids="selIds"
      :selected-platforms="selPlatforms"
      :selected-types="selTypes"
      :target="bulkEditTarget ?? undefined"
      :proxies="proxies"
      :groups="groups"
      @close="showBulkEdit = false"
      @updated="handleBulkUpdated"
    />
    <TempUnschedStatusModal v-if="tempUnschedModalMounted" :show="showTempUnsched" :account="tempUnschedAcc" :recovering="!!tempUnschedAcc && isRecovering(tempUnschedAcc.id)" @close="showTempUnsched = false" @recover="recoverOne" />
    <SchedQualityLayers
      :on-iq-check="openIqRecordsById"
      :on-edit="openEditById"
      :on-recovered="refreshAfterRecover"
      :account="panelAccount"
      :group-info="panelGroupInfo"
      :anchor="panelAnchor"
    />
    <SchedBatchDialog :action="sqBatch.action" :accounts="sqBatch.accounts" :anchor="sqBatch.anchor" :groups="sqOpenAIGroups" @close="sqBatch.action = null" @busy="(running) => (sqBatchBusy = running)" @done="reload" @recover="handleBatchRecover" />
    <ConfirmDialog :show="showDeleteDialog" :title="t('admin.accounts.deleteAccount')" :message="t('admin.accounts.deleteConfirm', { name: deletingAcc?.name })" :confirm-text="t('common.delete')" :cancel-text="t('common.cancel')" :danger="true" @confirm="confirmDelete" @cancel="showDeleteDialog = false" />
    <ConfirmDialog :show="showCreateShadowDialog" :title="t('admin.accounts.createSparkShadow')" :message="t('admin.accounts.createSparkShadowConfirm', { name: creatingShadowAcc?.name })" @confirm="confirmCreateSparkShadow" @cancel="showCreateShadowDialog = false" />
    <ConfirmDialog :show="showExportDataDialog" :title="t('admin.accounts.dataExport')" :message="t('admin.accounts.dataExportConfirmMessage')" :confirm-text="t('admin.accounts.dataExportConfirm')" :cancel-text="t('common.cancel')" @confirm="handleExportData" @cancel="showExportDataDialog = false">
      <label class="flex items-center gap-2 text-sm text-fg-primary">
        <input v-model="includeProxyOnExport" type="checkbox" class="acc-check" />
        <span>{{ t('admin.accounts.dataExportIncludeProxies') }}</span>
      </label>
    </ConfirmDialog>
    <ErrorPassthroughRulesModal v-if="errorPassthroughModalMounted" :show="showErrorPassthrough" :readonly="!canManage" @close="showErrorPassthrough = false" />
    <TLSFingerprintProfilesModal v-if="tlsProfilesModalMounted" :show="showTLSFingerprintProfiles" :readonly="!canManage" @close="showTLSFingerprintProfiles = false" />
    <TotpStepUpDialog :controller="accountExportStepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
/**
 * Account management (spec 4): the table on the live engine (useAccountsLiveTable), the toolbar, and the page's
 * actions and dialogs. Cells and the toolbar live in components/admin/account/table; rows are never copied into
 * dialogs that edit them (each dialog gets its own snapshot or loads the account).
 */
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useDebounceFn, useEventListener, useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { IQ_RUN_BATCH_LIMIT } from '@/api/admin/accounts'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import PendingChangesPill from '@/components/common/PendingChangesPill.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import AccountActionMenu from '@/components/admin/account/AccountActionMenu.vue'
import AccountBulkActionsBar from '@/components/admin/account/AccountBulkActionsBar.vue'
import AccountStreamOnlyBadge from '@/components/admin/account/AccountStreamOnlyBadge.vue'
import { IQ_BUILTIN_MODELS, iqRunSummary, iqTargetsSwitched } from '@/components/admin/account/iqCheckPresentation'
import SchedBatchDialog, { type SchedBatchAction } from '@/components/admin/account/quality/SchedBatchDialog.vue'
import SchedPauseMark from '@/components/admin/account/quality/SchedPauseMark.vue'
import SchedQualityLayers from '@/components/admin/account/quality/SchedQualityLayers.vue'
import { useSchedLayerTasks } from '@/composables/useSchedLayerData'
import AccountGroupPicker from '@/components/admin/account/table/AccountGroupPicker.vue'
import AccountGroupPills from '@/components/admin/account/table/AccountGroupPills.vue'
import AccountIQCell from '@/components/admin/account/table/AccountIQCell.vue'
import AccountNameCell from '@/components/admin/account/table/AccountNameCell.vue'
import AccountRecentCell from '@/components/admin/account/table/AccountRecentCell.vue'
import AccountStatusCell from '@/components/admin/account/table/AccountStatusCell.vue'
import AccountSwitch from '@/components/admin/account/table/AccountSwitch.vue'
import AccountTodayCell from '@/components/admin/account/table/AccountTodayCell.vue'
import AccountUsageFit from '@/components/admin/account/table/AccountUsageFit.vue'
import AccountToolsMenu, { type AccountToolAction } from '@/components/admin/account/table/AccountToolsMenu.vue'
import AccountsToolbar, { type ToolbarFilters } from '@/components/admin/account/table/AccountsToolbar.vue'
import { accountMatchesFilters, type AccountListFilters } from '@/components/admin/account/table/accountFilters'
import { orderGroups } from '@/components/admin/account/table/accountGroups'
import { accountRowPatch } from '@/components/admin/account/table/accountRow'
import { useAccountsLiveTable, type LiveAccount } from '@/components/admin/account/table/useAccountsLiveTable'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountPriorityCell from '@/components/account/AccountPriorityCell.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import UpstreamBalanceCell from '@/components/account/UpstreamBalanceCell.vue'
import UpstreamBillingRateCell from '@/components/account/UpstreamBillingRateCell.vue'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { canRecover, isRecovering, useAccountRecovery } from '@/composables/useAccountRecovery'
import { useColumnOrder } from '@/composables/useColumnOrder'
import { defineLazyComponent, prefetchWhenIdle, useLazyMount } from '@/composables/useLazyMount'
import { useLiveClock } from '@/composables/useLiveClock'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { openSchedDrawer, trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import { isStepUpBlocked, isStepUpCancelled, stepUpBlockReason, useStepUp } from '@/composables/useStepUp'
import { useSwipeSelect, type SwipeSelectVirtualContext } from '@/composables/useSwipeSelect'
import { useTableSelection } from '@/composables/useTableSelection'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useGroupCategoriesStore } from '@/stores/groupCategories'
import type { Account, AccountListItem, AccountPlatform, AccountType, AccountUsageInfo, AdminGroup, ClaudeModel, IQRunAccount, IQRunTarget, IQTargetSummary, Proxy as AccountProxy, UpstreamBillingProbeSnapshot } from '@/types'
import type { SelectOption } from '@/components/common/Select.vue'
import { fetchAllAccountIds } from '@/utils/accountSelection'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { formatMultiplier } from '@/utils/formatters'
import { proxyExpiryBadgeClass, proxyExpiryLabelKey } from '@/utils/proxyExpiry'
import { vTip } from '@/utils/tip'
import { getAccountPlanType, getAntigravityTier, getOpenAIAuthMode, getOpenAICompactMeta, getOpenAICompactTitle } from './accountRowMeta'

const IQCheckResultsModal = defineLazyComponent(() => import('@/components/admin/account/IQCheckResultsModal.vue'))
// Dialogs load on first use; the create and edit dialogs are prefetched while the page is idle.
const loadCreateAccountModal = () => import('@/components/account/CreateAccountModal.vue')
const loadEditAccountModal = () => import('@/components/account/EditAccountModal.vue')
const CreateAccountModal = defineLazyComponent(loadCreateAccountModal)
const EditAccountModal = defineLazyComponent(loadEditAccountModal)
const BulkEditAccountModal = defineLazyComponent(() => import('@/components/account/BulkEditAccountModal.vue'))
const SyncFromCrsModal = defineLazyComponent(() => import('@/components/account/SyncFromCrsModal.vue'))
const TempUnschedStatusModal = defineLazyComponent(() => import('@/components/account/TempUnschedStatusModal.vue'))
const OpenAILoginImportDialog = defineLazyComponent(() => import('@/components/admin/account/OpenAILoginImportDialog.vue'))
const OpenAITotpDialog = defineLazyComponent(() => import('@/components/admin/account/OpenAITotpDialog.vue'))
const ImportDataModal = defineLazyComponent(() => import('@/components/admin/account/ImportDataModal.vue'))
const ReAuthAccountModal = defineLazyComponent(() => import('@/components/admin/account/ReAuthAccountModal.vue'))
const AccountTestModal = defineLazyComponent(() => import('@/components/admin/account/AccountTestModal.vue'))
const AccountStatsModal = defineLazyComponent(() => import('@/components/admin/account/AccountStatsModal.vue'))
const ScheduledTestsPanel = defineLazyComponent(() => import('@/components/admin/account/ScheduledTestsPanel.vue'))
const ErrorPassthroughRulesModal = defineLazyComponent(() => import('@/components/admin/ErrorPassthroughRulesModal.vue'))
const TLSFingerprintProfilesModal = defineLazyComponent(() => import('@/components/admin/TLSFingerprintProfilesModal.vue'))

const { t, te } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const groupCategories = useGroupCategoriesStore()
const { canManage, isSuperAdmin } = useAdminPermission('accounts')
// Account management includes placing accounts into groups and removing them.
const canPlaceGroups = canManage
const now = useLiveClock()

const proxies = ref<AccountProxy[]>([])
const groups = ref<AdminGroup[]>([])
const groupsById = computed(() => new Map(groups.value.map((group) => [group.id, group])))
/** The row's groups in the groups page's order. */
const groupsOf = (row: Pick<AccountListItem, 'group_ids'>) =>
  orderGroups((row.group_ids ?? []).flatMap((id) => groupsById.value.get(id) ?? []))

// ---- Columns: visibility and order (new storage keys: old settings start over, today's stats shown) ----
const HIDDEN_COLUMNS_KEY = 'account-hidden-columns-v2'
const ACCOUNT_COLUMN_ORDER_KEY = 'account-column-order-v2'
const DEFAULT_HIDDEN_COLUMNS = ['id', 'platform_type', 'priority', 'usage', 'proxy', 'rate_multiplier', 'upstream_balance', 'last_used_at', 'created_at', 'expires_at', 'notes']
const hiddenColumns = reactive(new Set<string>())
const loadSavedColumns = () => {
  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    const keys = saved ? (JSON.parse(saved) as string[]) : DEFAULT_HIDDEN_COLUMNS
    keys.forEach((key) => hiddenColumns.add(key))
  } catch {
    DEFAULT_HIDDEN_COLUMNS.forEach((key) => hiddenColumns.add(key))
  }
}
loadSavedColumns()
const saveColumns = () => {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
  } catch (error) {
    console.error('Failed to save columns:', error)
  }
}
const isColumnVisible = (key: string) => !hiddenColumns.has(key)

// Sorting (kept from before: last use, newest first, unless a sort was chosen)
const ACCOUNT_SORT_STORAGE_KEY = 'account-table-sort-v2'
type SortOrder = 'asc' | 'desc'
const SORTABLE_KEYS = new Set(['id', 'name', 'status', 'schedulable', 'priority', 'rate_multiplier', 'upstream_billing_rate', 'last_used_at', 'created_at', 'expires_at'])
const loadInitialSort = (): { sort_by: string; sort_order: SortOrder } => {
  const fallback = { sort_by: 'last_used_at', sort_order: 'desc' as SortOrder }
  try {
    const parsed = JSON.parse(localStorage.getItem(ACCOUNT_SORT_STORAGE_KEY) ?? 'null') as { key?: string; order?: string } | null
    if (!parsed || typeof parsed.key !== 'string' || !SORTABLE_KEYS.has(parsed.key)) return fallback
    return { sort_by: parsed.key, sort_order: parsed.order === 'desc' ? 'desc' : 'asc' }
  } catch {
    return fallback
  }
}
const sortState = reactive(loadInitialSort())

// ---- Filters, search and the page ----
const filters = reactive<Required<ToolbarFilters>>({ platform: '', status: '', group: '', sched_quality: '', type: '', privacy_mode: '', iq_status: '', group_category: '' })
const search = ref('')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize() })
const listFilters = (): AccountListFilters => ({ ...filters, search: search.value.trim() })

const qualityMeta = ref<{ global_enabled: boolean; stale: boolean } | null>(null)
/** The page the pagination bar shows: the one whose rows are on screen (it follows them, not the click). */
const shownPage = reactive({ page: pagination.page, page_size: pagination.page_size })
// The quality panel's and the group board's data come with the table's beat (spec 7f-1).
const schedLayers = useSchedLayerTasks()
const live = useAccountsLiveTable({
  page: () => ({ page: pagination.page, pageSize: pagination.page_size }),
  filters: listFilters,
  sort: () => ({ sort_by: sortState.sort_by, sort_order: sortState.sort_order }),
  wants: {
    // Managers need the summary even with the quality columns hidden: the row menu's "recover state" reads it. The
    // quality filter needs it to tell whether a row still matches.
    quality: () => canManage.value || isColumnVisible('groups') || isColumnVisible('sq_recent') || !!filters.sched_quality,
    today: () => isColumnVisible('today_stats') || isColumnVisible('usage'),
    balance: () => isColumnVisible('upstream_balance'),
    usage: () => isColumnVisible('usage')
  },
  matches: (row) =>
    accountMatchesFilters(row, listFilters(), { now: () => now.value, matchesCategory: groupCategories.matchesFilter, quality: row.quality }),
  onQualityMeta: (meta) => {
    qualityMeta.value = meta
  },
  onPageShown: (page) => Object.assign(shownPage, { page: page.page, page_size: page.pageSize }),
  tasks: schedLayers.tasks
})
const table = live.table
schedLayers.bind(table, (id) => table.record(id))
const { rows, loading, total, pending, status: liveStatus } = table
const accountRowClass = (row: LiveAccount) => (table.transitioning.value || table.isDimmed(row.id) ? 'opacity-60' : undefined)
const changesText = (n: string) => t('admin.accounts.table.changes', { n })

const dataTableRef = ref<InstanceType<typeof DataTable> | null>(null)
const toolbarRef = ref<InstanceType<typeof AccountsToolbar> | null>(null)
const accountTableRef = ref<HTMLElement | null>(null)

/** A failure toast: the action's own words, then the backend's reason when it gave one (spec 7e-13). */
const showFailure = (action: string, error: unknown) => {
  const reason = extractApiErrorMessage(error, '').trim()
  appStore.showError(reason && reason !== action ? t('admin.accounts.failedWithReason', { action, reason }) : action)
}
const showLoadError = (error: unknown) => {
  console.error('Failed to load accounts:', error)
  showFailure(t('common.error'), error)
}
/** An active load: rows stay on screen and are replaced in place. */
const reload = () => table.reload().catch(showLoadError)
const applyPending = () => table.applyPending().catch(showLoadError)

/** A filter change (or a search): back to the first page and the first row, then load. */
const applyFilters = (next?: ToolbarFilters) => {
  if (next) Object.assign(filters, next)
  clearSelection()
  pagination.page = 1
  dataTableRef.value?.scrollToTop?.()
  return reload()
}
const debouncedSearch = useDebounceFn(() => applyFilters(), 300)
const onSearchInput = (value: string) => {
  search.value = value
  void debouncedSearch()
}
const handleSort = (key: string, order: SortOrder) => {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  void reload()
}
const handlePageChange = (page: number) => {
  pagination.page = Math.max(1, Math.min(page, live.pages() || 1))
  void reload()
}
const handlePageSizeChange = (size: number) => {
  pagination.page_size = size
  pagination.page = 1
  void reload()
}
/** Sync now and re-sort by the current sort and filters; the usage windows skip their cache. */
const handleManualRefresh = () => {
  live.forceUsageRefresh()
  void loadUpstreamBillingProbeGlobalState()
  return reload()
}

// ---- Columns ----
const allColumns = computed(() => [
  { key: 'select', label: '', sortable: false },
  { key: 'name', label: t('admin.accounts.columns.name'), sortable: true },
  { key: 'status', label: t('admin.accounts.columns.status'), sortable: true },
  { key: 'schedulable', label: t('admin.accounts.columns.schedulable'), sortable: true },
  { key: 'groups', label: t('admin.accounts.columns.groups'), sortable: false },
  { key: 'sq_recent', label: t('admin.schedQuality.list.recent'), sortable: false },
  { key: 'iq_check', label: t('admin.accounts.iqCheck'), sortable: false },
  { key: 'capacity', label: t('admin.accounts.columns.capacity'), sortable: false },
  { key: 'upstream_billing_rate', label: t('admin.accounts.columns.upstreamBillingRate'), sortable: true },
  { key: 'today_stats', label: t('admin.accounts.columns.todayStats'), sortable: false },
  { key: 'id', label: t('admin.accounts.columns.id'), sortable: true },
  { key: 'platform_type', label: t('admin.accounts.columns.platformType'), sortable: false },
  { key: 'priority', label: t('admin.accounts.columns.priority'), sortable: true },
  { key: 'usage', label: t('admin.accounts.columns.usageWindows'), sortable: false },
  { key: 'proxy', label: t('admin.accounts.columns.proxy'), sortable: false },
  { key: 'rate_multiplier', label: t('admin.accounts.columns.billingRateMultiplier'), sortable: true },
  { key: 'upstream_balance', label: t('admin.accounts.columns.upstreamBalance'), sortable: false },
  { key: 'last_used_at', label: t('admin.accounts.columns.lastUsed'), sortable: true },
  { key: 'created_at', label: t('admin.accounts.columns.createdAt'), sortable: true },
  { key: 'expires_at', label: t('admin.accounts.columns.expiresAt'), sortable: true },
  { key: 'notes', label: t('admin.accounts.columns.notes'), sortable: false },
  { key: 'actions', label: t('admin.accounts.columns.actions'), sortable: false }
].map((column) => ({ ...column, class: `acol acol-${column.key}`, headerClass: `acol acol-${column.key}` })))
const PINNED = new Set(['select', 'name', 'actions'])
const toggleableColumns = computed(() => allColumns.value.filter((column) => !PINNED.has(column.key)))
const toggleableColumnKeys = computed(() => toggleableColumns.value.map((column) => column.key))
const { orderedKeys: columnOrder, isCustomized: columnOrderCustomized, setOrder: setColumnOrder, resetOrder: resetColumnOrder } = useColumnOrder(ACCOUNT_COLUMN_ORDER_KEY, toggleableColumnKeys)
const orderedToggleableColumns = computed(() => {
  const byKey = new Map(toggleableColumns.value.map((column) => [column.key, column]))
  return columnOrder.value.flatMap((key) => byKey.get(key) ?? [])
})
const columnsAtDefault = computed(() =>
  !columnOrderCustomized.value && toggleableColumnKeys.value.every((key) => hiddenColumns.has(key) === DEFAULT_HIDDEN_COLUMNS.includes(key))
)
const cols = computed(() => {
  const pinned = (key: string) => allColumns.value.filter((column) => column.key === key)
  return [...pinned('select'), ...pinned('name'), ...orderedToggleableColumns.value.filter((column) => !hiddenColumns.has(column.key)), ...pinned('actions')]
})
// A column that loads its own data asks for it once it is shown again.
const COLUMN_SOURCES: Record<string, string> = { groups: 'quality', sq_recent: 'quality', today_stats: 'today', usage: 'usage', upstream_balance: 'balance' }
const toggleColumn = (key: string) => {
  const wasHidden = hiddenColumns.has(key)
  if (wasHidden) hiddenColumns.delete(key)
  else hiddenColumns.add(key)
  saveColumns()
  if (wasHidden && COLUMN_SOURCES[key]) void table.refresh(COLUMN_SOURCES[key])
}
const resetColumns = () => {
  const shown = toggleableColumnKeys.value.filter((key) => hiddenColumns.has(key) && !DEFAULT_HIDDEN_COLUMNS.includes(key))
  resetColumnOrder()
  hiddenColumns.clear()
  DEFAULT_HIDDEN_COLUMNS.forEach((key) => hiddenColumns.add(key))
  try {
    localStorage.removeItem(HIDDEN_COLUMNS_KEY)
  } catch (error) {
    console.error('Failed to reset columns:', error)
  }
  new Set(shown.flatMap((key) => COLUMN_SOURCES[key] ?? [])).forEach((name) => void table.refresh(name))
}

// Column widths (prototype): fixed widths in two sizes; from 1440 the ten default columns fit with no sideways scroll.
// Wider tables share the room left over evenly between the columns instead of the name column (at most 300px).
const COLUMN_WIDTHS: Record<string, [number, number]> = {
  select: [36, 40], status: [128, 140], schedulable: [52, 60], groups: [176, 176], sq_recent: [96, 120], iq_check: [148, 164],
  capacity: [132, 144], upstream_billing_rate: [100, 124], today_stats: [132, 152], actions: [84, 104], id: [80, 88],
  platform_type: [168, 180], priority: [112, 120], usage: [220, 240], proxy: [160, 176], rate_multiplier: [100, 112],
  upstream_balance: [140, 152], last_used_at: [120, 132], created_at: [150, 160], expires_at: [150, 160], notes: [180, 200]
}
const NAME_MIN = 140
const NAME_MAX = 300
const tableWidth = ref(0)
useResizeObserver(accountTableRef, (entries) => {
  tableWidth.value = entries[0]?.contentRect.width ?? 0
})
const columnLayout = computed(() => {
  const keys = cols.value.map((column) => column.key).filter((key) => key !== 'name')
  const sum = (size: 0 | 1) => keys.reduce((total, key) => total + (COLUMN_WIDTHS[key]?.[size] ?? 120), 0)
  const roomy = tableWidth.value >= sum(1) + NAME_MAX
  const size = roomy ? 1 : 0
  const share = Math.max(0, (tableWidth.value - 2 - sum(size) - NAME_MAX) / keys.length)
  const vars: Record<string, string> = { '--acc-share': `${share}px`, '--acc-min': `${sum(size) + NAME_MIN}px` }
  for (const key of keys) vars[`--acc-w-${key}`] = `${COLUMN_WIDTHS[key]?.[size] ?? 120}px`
  return { roomy, vars }
})

// ---- Selection ----
const { selectedSet, selectedIds: selIds, allVisibleSelected, isSelected, setSelectedIds, select, deselect, toggle: toggleSel, clear: clearSelectedIds, removeMany: removeSelectedAccounts, toggleVisible, selectVisible: selectCurrentPage, batchUpdate } =
  useTableSelection<LiveAccount>({ rows, getId: (account) => account.id })
const selectingAllResults = ref(false)
const selectedAllResultIDs = ref<Set<number> | null>(null)
let selectionRequestVersion = 0
const allResultsSelected = computed(() => {
  const snapshot = selectedAllResultIDs.value
  if (!snapshot || snapshot.size === 0 || snapshot.size !== selectedSet.value.size) return false
  return Array.from(snapshot).every((id) => selectedSet.value.has(id))
})
const clearSelection = () => {
  selectionRequestVersion++
  selectingAllResults.value = false
  selectedAllResultIDs.value = null
  clearSelectedIds()
}
const selectedRows = computed(() => rows.value.filter((account) => isSelected(account.id)))
const selPlatforms = computed<AccountPlatform[]>(() => [...new Set(selectedRows.value.map((account) => account.platform))])
const selTypes = computed<AccountType[]>(() => [...new Set(selectedRows.value.map((account) => account.type))])
// Shift+click on a checkbox sets every row between it and the last one clicked, in the order shown.
let selectionAnchor: number | null = null
let rowCheckShift = false
const noteRowCheckShift = (event: MouseEvent) => {
  rowCheckShift = event.shiftKey
}
const handleRowCheck = (id: number, event: Event) => {
  const ids = rows.value.map((account) => account.id)
  const from = selectionAnchor === null ? -1 : ids.indexOf(selectionAnchor)
  const to = ids.indexOf(id)
  const shift = rowCheckShift
  rowCheckShift = false
  if (shift && from !== -1 && to !== -1 && from !== to) {
    const range = ids.slice(Math.min(from, to), Math.max(from, to) + 1)
    const checked = (event.target as HTMLInputElement).checked
    batchUpdate((draft) => range.forEach((rowId) => (checked ? draft.add(rowId) : draft.delete(rowId))))
  } else {
    toggleSel(id)
  }
  selectionAnchor = id
}
const swipeVirtualContext: SwipeSelectVirtualContext = {
  getVirtualizer: () => dataTableRef.value?.virtualizer ?? null,
  getSortedData: () => dataTableRef.value?.sortedData ?? rows.value,
  getRowId: (row: any) => row.id
}
useSwipeSelect(accountTableRef, {
  isSelected,
  select: (id) => { if (canManage.value) select(id) },
  deselect: (id) => { if (canManage.value) deselect(id) },
  batchUpdate: (updater) => { if (canManage.value) batchUpdate(updater) }
}, swipeVirtualContext)

// ---- Rows: merges from action answers, edits through the engine ----
/** An action's answer for one account: its fields are merged (the live row keeps the rest, e.g. concurrency). */
/** A full account into its row: list fields it leaves out are written empty, its groups structure stays out (7d-1). */
const mergeAccount = (account: Account) => table.mergeRows([[account.id, accountRowPatch<LiveAccount>(account)]])
const handleAccountUpdated = (account: Account) => {
  mergeAccount(account)
  live.forgetUsage([account.id])
  if (isColumnVisible('upstream_balance')) void table.refresh('balance')
}
const togglingSchedulable = reactive(new Set<number>())
const handleToggleSchedulable = async (row: LiveAccount, next: boolean) => {
  togglingSchedulable.add(row.id)
  try {
    await table.edit(row.id, 'schedulable', next, async () => {
      const updated = await adminAPI.accounts.setSchedulable(row.id, next)
      return { schedulable: updated?.schedulable ?? next }
    })
  } catch (error) {
    console.error('Failed to toggle schedulable:', error)
    showFailure(t('admin.accounts.failedToToggleSchedulable'), error)
  } finally {
    togglingSchedulable.delete(row.id)
  }
}
// The list switch turns both built-in targets on or off; custom targets keep their own setting. Like the scheduling
// switch it flips at once (an account without built-in targets yet gets them on the row) and shows busy until the
// answer; a failure puts the old targets back (7e-1).
const togglingIQ = reactive(new Set<number>())
const handleToggleIQCheck = async (row: LiveAccount, enabled: boolean) => {
  togglingIQ.add(row.id)
  try {
    await table.edit(row.id, 'iq_targets', iqTargetsSwitched(row.iq_targets, enabled), async () => ({
      iq_targets: await adminAPI.accounts.setIQCheck(row.id, { targets: IQ_BUILTIN_MODELS.map((model) => ({ model, enabled })) })
    }))
  } catch (error) {
    console.error('Failed to toggle IQ check:', error)
    showFailure(t('admin.accounts.iqFailed'), error)
  } finally {
    togglingIQ.delete(row.id)
  }
}
const savePriority = async (row: LiveAccount, priority: number) => {
  await table.edit(row.id, 'priority', priority, async () => {
    const updated = await adminAPI.accounts.update(row.id, { priority })
    return { priority: updated.priority }
  })
}
const handleIQSummaryUpdated = (targets: IQTargetSummary[]) => {
  const account = iqRecordsAccount.value
  if (!account) return
  iqRecordsAccount.value = { ...account, iq_targets: targets }
  table.mergeRows([[account.id, { iq_targets: targets }]])
}
/** The usage cell asks again (a changed OpenAI snapshot, a probe): the next usage run skips the cache for it. */
const requestUsage = (account: Account, options?: { force?: boolean }) => {
  if (!options?.force) return
  live.forgetUsage([account.id])
  void table.refresh('usage')
}

// ---- Upstream billing: a probe updates its own cell only ----
const probingUpstreamBilling = reactive(new Set<number>())
const upstreamBillingProbeGloballyEnabled = ref<boolean | undefined>(undefined)
const loadUpstreamBillingProbeGlobalState = async () => {
  try {
    upstreamBillingProbeGloballyEnabled.value = (await adminAPI.accounts.getUpstreamBillingProbeSettings()).enabled
  } catch (error) {
    console.error('Failed to load upstream billing probe settings:', error)
  }
}
const patchProbeSnapshot = (id: number, snapshot: UpstreamBillingProbeSnapshot) => {
  const row = table.record(id)
  if (!row) return
  table.mergeRows([[id, {
    ...(typeof snapshot.synced_rate_multiplier === 'number' ? { rate_multiplier: snapshot.synced_rate_multiplier } : {}),
    extra: { ...row.extra, upstream_billing_probe: snapshot }
  }]])
}
const handleProbeUpstreamBilling = async (row: LiveAccount) => {
  if (!canManage.value || probingUpstreamBilling.has(row.id)) return
  probingUpstreamBilling.add(row.id)
  try {
    const result = await adminAPI.accounts.probeUpstreamBilling(row.id)
    if (result.snapshot) patchProbeSnapshot(row.id, result.snapshot)
  } catch (error) {
    console.error('Failed to probe upstream billing:', error)
    showFailure(t('admin.accounts.upstreamBilling.probeFailed'), error)
  } finally {
    probingUpstreamBilling.delete(row.id)
  }
}
const handleBulkProbeUpstreamBilling = async () => {
  const ids = [...selIds.value]
  if (ids.length === 0) return appStore.showError(t('admin.accounts.upstreamBilling.noEligibleAccounts'))
  if (ids.length > 20) return appStore.showError(t('admin.accounts.upstreamBilling.batchLimit'))
  ids.forEach((id) => probingUpstreamBilling.add(id))
  try {
    const results = await adminAPI.accounts.probeUpstreamBillingBatch(ids)
    results.forEach((result) => { if (result.snapshot) patchProbeSnapshot(result.account_id, result.snapshot) })
    const failed = results.filter((result) => result.error).length
    if (failed > 0) appStore.showError(t('admin.accounts.upstreamBilling.batchPartial', { success: results.length - failed, failed }))
    else appStore.showSuccess(t('admin.accounts.upstreamBilling.batchCompleted', { count: results.length }))
  } catch (error) {
    console.error('Failed to probe upstream billing in batch:', error)
    showFailure(t('admin.accounts.upstreamBilling.probeFailed'), error)
  } finally {
    ids.forEach((id) => probingUpstreamBilling.delete(id))
  }
}

// ---- Bulk actions ----
const handleBulkDelete = async () => {
  const ids = [...selIds.value]
  if (!confirm(t('admin.accounts.bulkActions.confirmDelete', { count: ids.length }))) return
  try {
    const result = await adminAPI.accounts.batchDelete(ids)
    if (result.failed > 0) {
      appStore.showError(t('admin.accounts.bulkActions.partialSuccess', { success: result.success, failed: result.failed }))
      setSelectedIds(result.failed_ids?.length ? result.failed_ids : ids)
    } else {
      appStore.showSuccess(t('admin.accounts.bulkActions.deleteSuccess', { count: result.success }))
      clearSelection()
    }
    await reload()
  } catch (error) {
    console.error('Failed to bulk delete accounts:', error)
    showFailure(t('admin.accounts.failedToDelete'), error)
  }
}
const handleBulkResetStatus = async () => {
  if (!confirm(t('common.confirm'))) return
  try {
    const result = await adminAPI.accounts.batchClearError(selIds.value)
    if (result.failed > 0) appStore.showError(t('admin.accounts.bulkActions.partialSuccess', { success: result.success, failed: result.failed }))
    else {
      appStore.showSuccess(t('admin.accounts.bulkActions.resetStatusSuccess', { count: result.success }))
      clearSelection()
    }
    void reload()
  } catch (error) {
    console.error('Failed to bulk reset status:', error)
    showFailure(t('common.error'), error)
  }
}
const handleBulkRefreshToken = async () => {
  if (!confirm(t('common.confirm'))) return
  const ids = [...selIds.value]
  try {
    const result = await adminAPI.accounts.batchRefresh(ids)
    if (result.failed > 0) {
      appStore.showError(t('admin.accounts.bulkActions.partialSuccess', { success: result.success, failed: result.failed }))
      const failedIds = result.errors?.map((error) => error.account_id) ?? []
      setSelectedIds(failedIds.length > 0 ? failedIds : ids)
    } else {
      appStore.showSuccess(t('admin.accounts.bulkActions.refreshTokenSuccess', { count: result.success }))
      clearSelection()
    }
    void reload()
  } catch (error) {
    console.error('Failed to bulk refresh token:', error)
    showFailure(t('common.error'), error)
  }
}
type BulkResult = { success?: number; failed?: number; success_ids?: number[]; failed_ids?: number[]; results?: Array<{ account_id: number; success: boolean }> }
/** Which accounts a bulk scheduling update changed, from whichever shape the answer has. */
const bulkOutcome = (result: BulkResult, ids: number[]) => {
  const successIds = result.success_ids ?? result.results?.filter((item) => item.success).map((item) => item.account_id) ?? []
  const failedIds = result.failed_ids ?? result.results?.filter((item) => !item.success).map((item) => item.account_id) ?? []
  const hasCounts = typeof result.success === 'number' || typeof result.failed === 'number'
  const successCount = typeof result.success === 'number' ? result.success : successIds.length
  const failedCount = typeof result.failed === 'number' ? result.failed : failedIds.length
  if (!successIds.length && !failedIds.length && hasCounts && failedCount === 0 && successCount === ids.length && ids.length > 0) {
    return { successIds: ids, failedIds: [], successCount, failedCount, known: true }
  }
  return { successIds, failedIds, successCount, failedCount, known: successIds.length > 0 || failedIds.length > 0 || hasCounts }
}
/** Every selected switch flips at once and stays locked until the answer; the ones that failed roll back. */
const handleBulkToggleSchedulable = async (schedulable: boolean) => {
  const ids = [...selIds.value]
  if (!ids.length || ids.some((id) => togglingSchedulable.has(id))) return
  ids.forEach((id) => togglingSchedulable.add(id))
  const outcome = adminAPI.accounts.bulkUpdate(ids, { schedulable }).then((result) => bulkOutcome(result, ids))
  try {
    await Promise.allSettled(ids.map((id) => table.edit(id, 'schedulable', schedulable, async () => {
      const { successIds } = await outcome
      if (!successIds.includes(id)) throw new Error('not updated')
      return { schedulable }
    })))
    const { failedIds, successCount, failedCount, known } = await outcome
    if (!known) {
      appStore.showError(t('admin.accounts.bulkSchedulableResultUnknown'))
      setSelectedIds(ids)
      void reload()
    } else if (failedCount > 0) {
      appStore.showError(t('admin.accounts.bulkSchedulablePartial', { success: successCount, failed: failedCount }))
      setSelectedIds(failedIds.length ? failedIds : ids)
    } else {
      appStore.showSuccess(schedulable ? t('admin.accounts.bulkSchedulableEnabled', { count: successCount }) : t('admin.accounts.bulkSchedulableDisabled', { count: successCount }))
      clearSelection()
    }
  } catch (error) {
    console.error('Failed to bulk toggle schedulable:', error)
    showFailure(t('admin.accounts.failedToToggleSchedulable'), error)
  } finally {
    ids.forEach((id) => togglingSchedulable.delete(id))
  }
}
const buildFilterSnapshot = () => ({ ...listFilters(), sort_by: sortState.sort_by, sort_order: sortState.sort_order })
const handleSelectAllResults = async () => {
  if (selectingAllResults.value || total.value === 0) return
  const version = ++selectionRequestVersion
  selectingAllResults.value = true
  try {
    const ids = await fetchAllAccountIds((page, pageSize, requestFilters) => adminAPI.accounts.list(page, pageSize, requestFilters), buildFilterSnapshot())
    if (version !== selectionRequestVersion) return
    setSelectedIds(ids)
    selectedAllResultIDs.value = new Set(ids)
  } catch (error) {
    if (version !== selectionRequestVersion) return
    console.error('Failed to select all account results:', error)
    showFailure(t('admin.accounts.bulkActions.selectAllFailed'), error)
  } finally {
    if (version === selectionRequestVersion) selectingAllResults.value = false
  }
}
type BulkEditTarget =
  | { mode: 'selected'; accountIds: number[]; selectedPlatforms: AccountPlatform[]; selectedTypes: AccountType[] }
  | { mode: 'filtered'; filters: ReturnType<typeof buildFilterSnapshot>; previewCount: number; selectedPlatforms: AccountPlatform[]; selectedTypes: AccountType[] }
const showBulkEdit = ref(false)
const bulkEditTarget = ref<BulkEditTarget | null>(null)
const openBulkEditSelected = () => {
  bulkEditTarget.value = { mode: 'selected', accountIds: [...selIds.value], selectedPlatforms: [...selPlatforms.value], selectedTypes: [...selTypes.value] }
  showBulkEdit.value = true
}
const openBulkEditFiltered = async () => {
  const snapshot = buildFilterSnapshot()
  const preview = await adminAPI.accounts.list(1, 100, snapshot)
  bulkEditTarget.value = {
    mode: 'filtered',
    filters: snapshot,
    previewCount: preview.total,
    selectedPlatforms: [...new Set(preview.items.map((account) => account.platform))],
    selectedTypes: [...new Set(preview.items.map((account) => account.type))]
  }
  showBulkEdit.value = true
}
const handleBulkUpdated = () => {
  showBulkEdit.value = false
  bulkEditTarget.value = null
  clearSelection()
  void reload()
}
const handleDataImported = () => {
  showImportData.value = false
  void reload()
}

// ---- IQ runs ----
const reportIQRun = (results: IQRunTarget[] | IQRunAccount[]) => {
  const { queued, rejected, reasons } = iqRunSummary(results)
  const reasonText = reasons.map((reason) => (te(`admin.accounts.iqReasons.${reason}`) ? t(`admin.accounts.iqReasons.${reason}`) : reason)).join(', ')
  if (rejected) appStore.showError(t('admin.accounts.iqRunPartial', { queued, rejected, reasons: reasonText }))
  else appStore.showSuccess(t('admin.accounts.iqQueued', { count: queued }))
}
const handleRunIQCheck = async (account: Account) => {
  try {
    reportIQRun(await adminAPI.accounts.runIQCheck(account.id))
    void table.refresh('rows')
  } catch (error) {
    showFailure(t('admin.accounts.iqFailed'), error)
  }
}
const runningIQBatch = ref(false)
const handleBulkRunIQCheck = async () => {
  // Selection can span pages; the server rejects non-OpenAI accounts per target.
  const ids = [...selIds.value]
  if (ids.length > IQ_RUN_BATCH_LIMIT) return appStore.showError(t('admin.accounts.iqBatchLimit', { limit: IQ_RUN_BATCH_LIMIT }))
  runningIQBatch.value = true
  try {
    reportIQRun(await adminAPI.accounts.runIQCheckBatch(ids))
    void table.refresh('rows')
  } catch (error) {
    showFailure(t('admin.accounts.iqFailed'), error)
  } finally {
    runningIQBatch.value = false
  }
}

// ---- Quality: the panel, batches and recovery ----
const { changed: sqChanged } = useSchedLayers()
const sqBatch = reactive<{ action: SchedBatchAction | null; accounts: Array<{ id: number; name: string }>; anchor: HTMLElement | null }>({ action: null, accounts: [], anchor: null })
const sqBatchBusy = ref(false)
// An exemption, its removal or a recovery (in the panel, the row menu or a batch): the pills' summary is asked again
// at once, so the dots change without waiting for the 5 s beat.
// markSchedChanged() waits for it, so a recovery holds its lock until the new summary is on screen.
watch(sqChanged, () => trackSchedReload(table.refresh('quality')))
/** The panel reads the row for the platform line and the upstream ratio. */
const panelAccount = (id: number) => table.record(id)
const panelGroupInfo = (id: number) => groupsById.value.get(id)
/** The panel's top edge follows the table's. */
const panelAnchor = () => accountTableRef.value
const sqOpenAIGroups = computed(() => groups.value.filter((group) => group.platform === 'openai').map((group) => ({ id: group.id, name: group.name })))
/** The pill's dot: the quality panel on that group, with the tab of its model at the back when it has one. */
const openQuality = (row: LiveAccount, groupId: number) => {
  const model = row.quality?.substandard.find((item) => item.group_id === groupId && item.effective)?.model ?? null
  openSchedDrawer({ id: row.id, name: row.name }, { groupId, model })
}
const openSchedBatch = (action: SchedBatchAction, anchor: HTMLElement) => {
  if (sqBatchBusy.value || sqBatch.action !== null) return
  sqBatch.accounts = selIds.value.map((id) => ({ id, name: table.record(id)?.name ?? `#${id}` }))
  sqBatch.anchor = anchor
  sqBatch.action = action
}
const { recover: recoverAccount, recoverMany, batchRunning: recoveryBatchRunning } = useAccountRecovery()
const menuRecoverable = (account: Account | null) => !!account && canRecover(account, table.record(account.id)?.quality, Date.now())
/**
 * After a recovery the page's rows are read again through the live table, which orders this read after every earlier
 * one (its own retries, merges and 3 s refreshes included); the summary comes in through markSchedChanged (7d-9).
 * Under the quality filter the page loads again instead. A summary asked for during a load only runs once the load is
 * over, so the summary after the load is waited for here: the recovery holds its lock until no row reads
 * "recoverable" from a summary older than the recovery (7d-8).
 */
const readAfterRecovery = async () => {
  if (!filters.sched_quality) return table.refresh('rows')
  const summary = live.qualityLanded()
  await reload()
  // No rows, no summary: nothing is left to read "recoverable".
  if (table.rows.value.length) await summary
}
const refreshAfterRecover = async (id: number) => {
  if (tempUnschedAcc.value?.id === id) showTempUnsched.value = false
  await readAfterRecovery()
}
const recoverOne = (account: Account) => recoverAccount({ id: account.id, name: account.name }, () => refreshAfterRecover(account.id))
// Recovered accounts leave the selection; failed and not-run ones stay for a retry.
const handleBatchRecover = async (ids: number[]) => {
  const { recovered } = await recoverMany(ids, readAfterRecovery)
  removeSelectedAccounts(recovered)
}

// ---- Dialogs and the row menu ----
const showCreate = ref(false)
const showEdit = ref(false)
const showSync = ref(false)
const showImportData = ref(false)
const showOpenAILogin = ref(false)
const openAITotpAccount = ref<Account | null>(null)
const showExportDataDialog = ref(false)
const includeProxyOnExport = ref(true)
const showTempUnsched = ref(false)
const showDeleteDialog = ref(false)
const showCreateShadowDialog = ref(false)
const showReAuth = ref(false)
const showTest = ref(false)
const showStats = ref(false)
const showErrorPassthrough = ref(false)
const showTLSFingerprintProfiles = ref(false)
const showSchedulePanel = ref(false)
const edAcc = ref<Account | null>(null)
const tempUnschedAcc = ref<Account | null>(null)
const deletingAcc = ref<Account | null>(null)
const creatingShadowAcc = ref<Account | null>(null)
const reAuthAcc = ref<Account | null>(null)
const testingAcc = ref<Account | null>(null)
const statsAcc = ref<Account | null>(null)
const scheduleAcc = ref<Account | null>(null)
const scheduleModelOptions = ref<SelectOption[]>([])
const iqRecordsAccount = ref<Account | null>(null)
const createModalMounted = useLazyMount(showCreate)
const editModalMounted = useLazyMount(showEdit)
const reAuthModalMounted = useLazyMount(showReAuth)
const testModalMounted = useLazyMount(showTest)
const statsModalMounted = useLazyMount(showStats)
const schedulePanelMounted = useLazyMount(showSchedulePanel)
const syncModalMounted = useLazyMount(showSync)
const importModalMounted = useLazyMount(showImportData)
const bulkEditModalMounted = useLazyMount(showBulkEdit)
const tempUnschedModalMounted = useLazyMount(showTempUnsched)
const errorPassthroughModalMounted = useLazyMount(showErrorPassthrough)
const tlsProfilesModalMounted = useLazyMount(showTLSFingerprintProfiles)
const menu = reactive<{ show: boolean; acc: Account | null; anchorRect: DOMRect | null }>({ show: false, acc: null, anchorRect: null })

// ---- The group picker of the groups column (spec 4.3) ----
const picker = reactive<{ account: Pick<Account, 'id' | 'name' | 'platform'> | null; anchor: HTMLElement | null; focusSearch: boolean }>({ account: null, anchor: null, focusSearch: false })
const pickerRef = ref<{ requestClose: () => Promise<boolean> } | null>(null)
/**
 * A pill's text: the picker beside its cell. On the same account's pills an open picker closes (saving a change); on
 * another account's it closes first and the new one opens once it has: after a save under way too (the latest click
 * wins), not when that save stops on a conflict to confirm.
 */
const openGroupsEditor = async (row: LiveAccount, event: MouseEvent) => {
  if (!canPlaceGroups.value) return
  const opener = event.currentTarget as HTMLElement | null
  const anchor = opener?.closest('td') ?? opener
  if (picker.account) {
    const same = picker.account.id === row.id
    const closed = (await pickerRef.value?.requestClose()) ?? true
    if (same || !closed) return
  }
  // The picker keeps its own copy: background updates do not reach what it edits.
  Object.assign(picker, { account: { id: row.id, name: row.name, platform: row.platform }, anchor, focusSearch: event.detail === 0 })
}
/** Closing the picker with a change: only this cell's groups, optimistically; a failure rolls back (the picker tells). */
const saveGroups = async (groupIds: number[], confirmMixedChannel: boolean) => {
  const id = picker.account?.id
  if (id === undefined) return
  await table.edit(id, 'group_ids', groupIds, async () => {
    const updated = await adminAPI.accounts.update(id, confirmMixedChannel ? { group_ids: groupIds, confirm_mixed_channel_risk: true } : { group_ids: groupIds })
    // Only this cell goes back into the row (7f-3); out of the last group the answer has no group_ids (7d-1).
    return { group_ids: updated.group_ids ?? [] }
  })
  // The dots follow the new groups at once.
  void table.refresh('quality')
}
const loadingDetails = new Set<number>()
const loadAccountDetails = async (account: Pick<AccountListItem, 'id'>): Promise<Account | null> => {
  if (loadingDetails.has(account.id)) return null
  loadingDetails.add(account.id)
  try {
    return await adminAPI.accounts.getById(account.id)
  } catch (error) {
    console.error('Failed to load account details:', error)
    showFailure(t('common.error'), error)
    return null
  } finally {
    loadingDetails.delete(account.id)
  }
}
const handleEdit = async (account: Pick<AccountListItem, 'id'>) => {
  const detail = await loadAccountDetails(account)
  if (!detail) return
  edAcc.value = detail
  showEdit.value = true
}
const openEditById = (id: number) => handleEdit({ id })
const openIqRecordsById = async (id: number) => {
  const detail = await loadAccountDetails({ id })
  if (detail) iqRecordsAccount.value = detail
}
const openMenu = (account: Account, event: MouseEvent) => {
  menu.acc = account
  menu.anchorRect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  menu.show = true
}
const handleTest = async (account: AccountListItem) => {
  const detail = await loadAccountDetails(account)
  if (!detail) return
  testingAcc.value = detail
  showTest.value = true
}
const handleViewStats = async (account: AccountListItem) => {
  const detail = await loadAccountDetails(account)
  if (!detail) return
  statsAcc.value = detail
  showStats.value = true
}
const closeTestModal = () => { showTest.value = false; testingAcc.value = null }
const closeStatsModal = () => { showStats.value = false; statsAcc.value = null }
const closeReAuthModal = () => { showReAuth.value = false; reAuthAcc.value = null }
const handleSchedule = async (account: Account) => {
  scheduleAcc.value = account
  scheduleModelOptions.value = []
  showSchedulePanel.value = true
  try {
    const models = await adminAPI.accounts.getAvailableModels(account.id)
    scheduleModelOptions.value = models.map((model: ClaudeModel) => ({ value: model.id, label: model.display_name || model.id }))
  } catch {
    scheduleModelOptions.value = []
  }
}
const closeSchedulePanel = () => { showSchedulePanel.value = false; scheduleAcc.value = null; scheduleModelOptions.value = [] }
const handleReAuth = (account: Account) => { reAuthAcc.value = { ...account }; showReAuth.value = true }
const handleShowTempUnsched = (account: Account) => { tempUnschedAcc.value = { ...account }; showTempUnsched.value = true }
const handleManageOpenAITotp = (account: Account) => {
  if (authStore.isAdmin && account.platform === 'openai' && account.type === 'oauth' && account.parent_account_id == null) openAITotpAccount.value = account
}
const duplicating = new Set<number>()
const handleDuplicateAccount = async (account: Account) => {
  if (duplicating.has(account.id)) return
  duplicating.add(account.id)
  try {
    const duplicate = await adminAPI.accounts.duplicate(account.id)
    appStore.showSuccess(t('admin.accounts.duplicateSuccess', { name: duplicate.name }))
    void reload()
  } catch (error: any) {
    console.error('Failed to duplicate account:', error)
    showFailure(t('admin.accounts.duplicateFailed'), error)
  } finally {
    duplicating.delete(account.id)
  }
}
const handleRefresh = async (account: Account) => {
  try {
    const result = await adminAPI.accounts.refreshCredentials(account.id)
    mergeAccount(result.account)
    if (result.warning) appStore.showWarning(result.message)
  } catch (error) {
    console.error('Failed to refresh credentials:', error)
    showFailure(t('admin.accounts.failedToRefresh'), error)
  }
}
const handleResetQuota = async (account: Account) => {
  try {
    mergeAccount(await adminAPI.accounts.resetAccountQuota(account.id))
    appStore.showSuccess(t('common.success'))
  } catch (error) {
    console.error('Failed to reset quota:', error)
    showFailure(t('common.error'), error)
  }
}
const privacyResult = (account: Account): { type: 'success' | 'error'; key: string } => {
  const mode = typeof account.extra?.privacy_mode === 'string' ? account.extra.privacy_mode : ''
  if (account.platform === 'openai') {
    if (mode === 'training_off') return { type: 'success', key: 'admin.accounts.privacyTrainingOff' }
    if (mode === 'training_set_cf_blocked') return { type: 'error', key: 'admin.accounts.privacyCfBlocked' }
    return { type: 'error', key: 'admin.accounts.privacyFailed' }
  }
  if (account.platform === 'antigravity') {
    return mode === 'privacy_set' ? { type: 'success', key: 'admin.accounts.privacyAntigravitySet' } : { type: 'error', key: 'admin.accounts.privacyAntigravityFailed' }
  }
  return { type: 'error', key: 'admin.accounts.privacyFailed' }
}
const handleSetPrivacy = async (account: Account) => {
  try {
    const updated = await adminAPI.accounts.setPrivacy(account.id)
    mergeAccount(updated)
    const result = privacyResult(updated)
    if (result.type === 'success') appStore.showSuccess(t(result.key))
    else appStore.showError(t(result.key))
  } catch (error: any) {
    console.error('Failed to set privacy:', error)
    showFailure(t('admin.accounts.privacyFailed'), error)
  }
}
const onRevertFallback = async (account: Account) => {
  try {
    await adminAPI.accounts.revertProxyFallback(account.id)
    appStore.showSuccess(t('admin.accounts.revertProxySuccess'))
    void reload()
  } catch (error: any) {
    console.error('Failed to revert proxy fallback:', error)
    showFailure(t('admin.accounts.revertProxyFailed'), error)
  }
}
const handleCreateSparkShadow = (account: Account) => {
  creatingShadowAcc.value = account
  showCreateShadowDialog.value = true
}
const confirmCreateSparkShadow = async () => {
  const account = creatingShadowAcc.value
  if (!account) return
  try {
    await adminAPI.accounts.createSparkShadow(account.id, { name: `${account.name} (Spark)` })
    showCreateShadowDialog.value = false
    creatingShadowAcc.value = null
    appStore.showSuccess(t('admin.accounts.createSparkShadowSuccess'))
    void reload()
  } catch (error: any) {
    console.error('Failed to create spark shadow:', error)
    showFailure(t('admin.accounts.createSparkShadowFailed'), error)
  }
}
const handleDelete = (account: Account) => { deletingAcc.value = account; showDeleteDialog.value = true }
/** A deleted account leaves the page at once and leaves the selection. */
const confirmDelete = async () => {
  const account = deletingAcc.value
  if (!account) return
  try {
    await adminAPI.accounts.delete(account.id)
    showDeleteDialog.value = false
    deletingAcc.value = null
    table.remove(account.id)
    removeSelectedAccounts([account.id])
  } catch (error) {
    console.error('Failed to delete account:', error)
    showFailure(t('admin.accounts.failedToDelete'), error)
  }
}

// ---- Export ----
const accountExportStepUp = useStepUp()
const exportingData = ref(false)
const exportTimestamp = () => {
  const date = new Date()
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`
}
const handleExportData = async () => {
  if (exportingData.value) return
  exportingData.value = true
  try {
    const payload = await accountExportStepUp.run(() => adminAPI.accounts.exportData(
      selIds.value.length > 0
        ? { ids: selIds.value, includeProxies: includeProxyOnExport.value }
        : { includeProxies: includeProxyOnExport.value, filters: buildFilterSnapshot() }
    ))
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `xy2api-account-${exportTimestamp()}.json`
    link.click()
    URL.revokeObjectURL(url)
    // Spark shadow accounts are left out of backups; say so when any were skipped.
    if (payload.skipped_shadows && payload.skipped_shadows > 0) appStore.showWarning(t('admin.accounts.dataExportedSkippedShadows', { count: payload.skipped_shadows }))
    else appStore.showSuccess(t('admin.accounts.dataExported'))
  } catch (error: any) {
    if (isStepUpCancelled(error)) {
      // Cancelled by the user: nothing to say.
    } else if (isStepUpBlocked(error)) {
      appStore.showError(stepUpBlockReason(error) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' ? t('stepUp.adminApiKeyForbidden') : t('stepUp.notEnabled'))
    } else {
      showFailure(t('admin.accounts.dataExportFailed'), error)
    }
  } finally {
    exportingData.value = false
    showExportDataDialog.value = false
  }
}

const handleToolAction = (action: AccountToolAction) => {
  switch (action) {
    case 'bulk-edit-filtered': return void openBulkEditFiltered()
    case 'sync-crs': showSync.value = true; return
    case 'import': showImportData.value = true; return
    case 'openai-login': if (authStore.isAdmin) showOpenAILogin.value = true; return
    case 'export': includeProxyOnExport.value = true; showExportDataDialog.value = true; return
    case 'error-passthrough': showErrorPassthrough.value = true; return
    case 'tls-profiles': showTLSFingerprintProfiles.value = true
  }
}

// ---- Cells: small helpers ----
const antigravityTier = (row: Account) => getAntigravityTier(row, t)
const openAICompactMeta = (row: Account) => getOpenAICompactMeta(row, t)
const openAICompactTitle = (row: Account) => getOpenAICompactTitle(row, t)
const proxyExpiryText = (proxy: AccountProxy) => {
  const { key, params } = proxyExpiryLabelKey(proxy.expires_at, proxy.status)
  return params ? t(key, params) : t(key)
}
const formatExpiresAt = (value: number | null) =>
  value ? formatDateTime(new Date(value * 1000), { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }, 'sv-SE') : '-'
const isExpired = (value: number | null) => !!value && value * 1000 <= now.value

// ---- Keys and scrolling ----
// "/" focuses the search box outside text fields; Escape clears the selection. Menus, dialogs and the panel handle
// Escape first (document listeners) and mark it handled.
const isTyping = (target: EventTarget | null) => target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
useEventListener(window, 'keydown', (event: KeyboardEvent) => {
  if (event.defaultPrevented || event.isComposing || document.body.classList.contains('modal-open')) return
  if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !isTyping(event.target)) {
    event.preventDefault()
    toolbarRef.value?.focusSearch()
  } else if (event.key === 'Escape' && selIds.value.length && !isTyping(event.target)) {
    clearSelection()
  }
})
// Scrolling the table closes the row menu (scrolling inside the menu does not).
useEventListener(window, 'scroll', (event: Event) => {
  if (event.target instanceof Element && event.target.closest('.action-menu-content')) return
  menu.show = false
}, true)

let cancelModalPrefetch: (() => void) | null = null
onMounted(async () => {
  void reload()
  void loadUpstreamBillingProbeGlobalState()
  cancelModalPrefetch = prefetchWhenIdle([loadCreateAccountModal, loadEditAccountModal], 3000)
  const [proxiesResult, groupsResult] = await Promise.allSettled([adminAPI.proxies.getAll(), adminAPI.groups.getAll()])
  if (proxiesResult.status === 'fulfilled') proxies.value = proxiesResult.value
  else console.error('Failed to load proxies:', proxiesResult.reason)
  if (groupsResult.status === 'fulfilled') groups.value = groupsResult.value
  else console.error('Failed to load groups:', groupsResult.reason)
})
onUnmounted(() => cancelModalPrefetch?.())
</script>

<style scoped>
/* This page only (7e-15): toolbar, table card and pagination sit closer, so a 1080px-high window shows at least ten
   whole rows. The shared page layout and the other pages keep their spacing. */
.accounts-layout.table-page-layout:not(.mobile-mode) {
  gap: 12px;
}
.accounts-layout .accounts-pagination {
  padding-top: 6px;
  padding-bottom: 6px;
}
/* The accounts table only (prototype): fixed 78px rows, 40px header, the column widths above; other table pages keep
   the shared table's own spacing. */
.account-table :deep(.table-wrapper) {
  --select-col-width: calc(var(--acc-w-select, 36px) + var(--acc-share, 0px));
}
.account-table :deep(.table-wrapper > table) {
  width: 100%;
  min-width: var(--acc-min, 1200px);
  table-layout: fixed;
}
.account-table :deep(thead th) {
  height: var(--header-height);
  padding: 0 var(--cell-pad-x);
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  letter-spacing: normal;
  line-height: var(--line-height-small);
  text-transform: none;
  white-space: nowrap;
}
/* Sort arrows only on the sorted column and on a sortable header under the pointer (7e-8). */
.account-table :deep(thead th .sort-indicator) {
  transition: opacity var(--motion-fast) var(--ease-out);
}
.account-table :deep(thead th .sort-indicator:not(.is-sorted)) {
  opacity: 0;
}
@media (hover: hover) {
  .account-table :deep(thead th:hover .sort-indicator) {
    opacity: 1;
  }
}
/* A header keeps to its column: a long label is cut, the tip has it whole. */
.account-table :deep(thead th > div) {
  min-width: 0;
}
.account-table :deep(.acol-head) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.account-table :deep(tbody td) {
  height: calc(var(--row-height) - 1px);
  padding: 0 var(--cell-pad-x);
  vertical-align: middle;
  white-space: nowrap;
}
.account-table :deep(th.acol-select),
.account-table :deep(td.acol-select) {
  padding: 0 0 0 12px;
}
.account-table :deep(th.acol-groups),
.account-table :deep(td.acol-groups) {
  padding: 0 8px;
}
.account-table :deep(th.acol-actions),
.account-table :deep(td.acol-actions) {
  text-align: right;
}
.account-table :deep(.acell) {
  display: flex;
  height: calc(var(--row-height) - 1px);
  min-width: 0;
  flex-direction: column;
  justify-content: center;
  gap: 2px;
  overflow: hidden;
}
.account-table :deep(.acell--row) {
  flex-direction: row;
  align-items: center;
  justify-content: flex-start;
}
.account-table :deep(.acell--end) {
  flex-direction: row;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
}
/* Column widths: each column's own width plus an even share of the room left over (see columnLayout). */
.account-table :deep(th.acol) {
  width: calc(var(--acc-w, 120px) + var(--acc-share, 0px));
}
.account-table :deep(th.acol-name) {
  width: auto;
}
.account-table :deep(th.acol-select) { --acc-w: var(--acc-w-select); }
.account-table :deep(th.acol-status) { --acc-w: var(--acc-w-status); }
.account-table :deep(th.acol-schedulable) { --acc-w: var(--acc-w-schedulable); }
.account-table :deep(th.acol-groups) { --acc-w: var(--acc-w-groups); }
.account-table :deep(th.acol-sq_recent) { --acc-w: var(--acc-w-sq_recent); }
.account-table :deep(th.acol-iq_check) { --acc-w: var(--acc-w-iq_check); }
.account-table :deep(th.acol-capacity) { --acc-w: var(--acc-w-capacity); }
.account-table :deep(th.acol-upstream_billing_rate) { --acc-w: var(--acc-w-upstream_billing_rate); }
.account-table :deep(th.acol-today_stats) { --acc-w: var(--acc-w-today_stats); }
.account-table :deep(th.acol-actions) { --acc-w: var(--acc-w-actions); }
.account-table :deep(th.acol-id) { --acc-w: var(--acc-w-id); }
.account-table :deep(th.acol-platform_type) { --acc-w: var(--acc-w-platform_type); }
.account-table :deep(th.acol-priority) { --acc-w: var(--acc-w-priority); }
.account-table :deep(th.acol-usage) { --acc-w: var(--acc-w-usage); }
.account-table :deep(th.acol-proxy) { --acc-w: var(--acc-w-proxy); }
.account-table :deep(th.acol-rate_multiplier) { --acc-w: var(--acc-w-rate_multiplier); }
.account-table :deep(th.acol-upstream_balance) { --acc-w: var(--acc-w-upstream_balance); }
.account-table :deep(th.acol-last_used_at) { --acc-w: var(--acc-w-last_used_at); }
.account-table :deep(th.acol-created_at) { --acc-w: var(--acc-w-created_at); }
.account-table :deep(th.acol-expires_at) { --acc-w: var(--acc-w-expires_at); }
.account-table :deep(th.acol-notes) { --acc-w: var(--acc-w-notes); }

.acc-check {
  width: 14px;
  height: 14px;
  margin: 0;
  border-radius: 3px;
  border-color: var(--color-line-control);
  background-color: var(--color-bg-control);
  color: theme('colors.primary.600');
  cursor: pointer;
}
.acc-check:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.acc-op {
  display: inline-grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
}
@media (hover: hover) {
  .acc-op:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
</style>
