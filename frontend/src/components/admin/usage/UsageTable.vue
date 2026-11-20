<template>
  <div :class="flat ? '' : 'card overflow-hidden'">
    <div
      v-if="showIpGeoToolbar"
      class="flex items-center justify-end gap-2 border-b border-gray-200 px-4 py-2 dark:border-dark-700"
    >
      <span v-if="pendingIpCount > 0" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('usage.ipGeo.pending', { count: pendingIpCount }) }}
      </span>
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:bg-primary-900/30"
        :disabled="ipGeoBatchLoading || pendingIpCount === 0"
        @click="handleBatchFetchIpGeo"
      >
        {{ ipGeoBatchLoading ? t('usage.ipGeo.batchFetching') : t('usage.ipGeo.batchFetch') }}
      </button>
    </div>
    <!-- Right above the table, under the IP lookup bar: the usage page's "N new records" anchor. -->
    <slot name="above-table" />
    <div class="overflow-auto">
      <DataTable
        ref="tableRef"
        :columns="columns"
        :data="data"
        :row-key="rowKey"
        :row-class="rowClass"
        :loading="loading"
        :server-side-sort="serverSideSort"
        :default-sort-key="defaultSortKey"
        :default-sort-order="defaultSortOrder"
        @sort="(key, order) => $emit('sort', key, order)"
      >
        <template #cell-user="{ row }">
          <div class="text-sm">
            <button
              v-if="row.user?.email"
              class="font-medium text-primary-600 underline decoration-dashed underline-offset-2 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              @click="$emit('userClick', row.user_id, row.user?.email)"
              :title="t('admin.usage.clickToViewBalance')"
            >
              {{ row.user.email }}
            </button>
            <span v-else class="text-gray-400 dark:text-gray-500">—</span>
            <span v-if="row.user?.deleted_at" class="ml-1 inline-flex items-center rounded px-1 py-px text-xs font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30">
              {{ t('admin.usage.userDeletedBadge') }}
            </span>
            <span class="ml-1 tabular-nums text-gray-500 dark:text-gray-400">#{{ row.user_id }}</span>
          </div>
        </template>

        <template #cell-api_key="{ row }">
          <span v-if="row.api_key?.name" class="text-sm text-gray-900 dark:text-white">{{ row.api_key.name }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
        </template>

        <template #cell-account="{ row }">
          <span class="text-sm" :class="row.account?.name ? 'text-gray-900 dark:text-white' : 'text-gray-400 dark:text-gray-500'">{{ row.account?.name || '—' }}</span>
          <!-- 后端 0.6.5 请求层：换号后成功的请求标"已救回"，换过号的标次数；与账号同一行 -->
          <span v-if="row.result === 2 || (row.switches ?? 0) > 0" class="ml-1.5 inline-flex gap-x-1.5 text-xs" data-testid="usage-request-marks">
            <span v-if="row.result === 2" class="text-amber-600 dark:text-amber-400">{{ t('admin.usage.resultRescued') }}</span>
            <span v-if="(row.switches ?? 0) > 0" class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.switches', { n: row.switches }) }}</span>
          </span>
        </template>

        <!-- 请求模型和发往上游的模型写在同一行（a → b）；上游答回的模型不一致属少见的异常，另起一行标出 -->
        <template #cell-model="{ row }">
          <div class="text-sm">
            <div data-testid="usage-model-steps">
              <template v-for="(step, i) in modelSteps(row)" :key="i">
                <span v-if="i > 0" class="mx-1 text-gray-400 dark:text-gray-500" aria-hidden="true">→</span>
                <span :class="i === 0 ? 'font-medium text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'">{{ step }}</span>
              </template>
            </div>
            <div
              v-if="hasModelAudit(row)"
              class="mt-0.5 text-xs"
              :class="isLikelyModelVariant(row) ? 'text-amber-600 dark:text-amber-400' : 'text-orange-600 dark:text-orange-400'"
              :title="modelAuditTitle(row, t)"
            >
              <span class="mr-1">{{ t('usage.upstreamResponseModel') }}:</span>{{ row.upstream_response_model }}
              <span
                class="ml-1 inline-flex rounded px-1 py-px text-xs font-medium leading-tight ring-1 ring-inset"
                :class="isLikelyModelVariant(row)
                  ? 'bg-amber-50 text-amber-700 ring-amber-200 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-500/30'
                  : 'bg-orange-50 text-orange-700 ring-orange-200 dark:bg-orange-500/10 dark:text-orange-300 dark:ring-orange-500/30'"
              >
                {{ isLikelyModelVariant(row) ? t('usage.modelVariant') : t('usage.modelMismatch') }}
              </span>
            </div>
          </div>
        </template>

        <template #cell-reasoning_effort="{ row }">
          <span data-testid="reasoning-effort-cell" class="text-sm">
            <span class="text-gray-900 dark:text-white" :class="{ 'font-medium': hasReasoningEffortMapping(row) }">{{ formatReasoningEffort(row.reasoning_effort) }}</span>
            <template v-if="hasReasoningEffortMapping(row)">
              <span class="mx-1 text-gray-400 dark:text-gray-500" aria-hidden="true">→</span>
              <span class="text-gray-500 dark:text-gray-400">{{ formatReasoningEffort(row.upstream_reasoning_effort) }}</span>
            </template>
          </span>
        </template>

        <!-- 入站端点；管理端上游端点不同时同一行“入站 → 上游”，两者带名称的完整值在悬停和明细里 -->
        <template #cell-endpoint="{ row }">
          <span class="text-xs" :title="endpointTitle(row)" data-testid="usage-endpoint">
            <span :class="endpointPair(row, showUpstreamEndpoint).inbound ? 'text-gray-700 dark:text-gray-300' : 'text-gray-400 dark:text-gray-500'">{{ endpointPair(row, showUpstreamEndpoint).inbound || '—' }}</span>
            <template v-if="upstreamDiffers(row)">
              <span class="mx-1 text-gray-400 dark:text-gray-500" aria-hidden="true">→</span>
              <span class="text-gray-500 dark:text-gray-400">{{ endpointPair(row, showUpstreamEndpoint).upstream }}</span>
            </template>
          </span>
        </template>

        <template #cell-group="{ row }">
          <div class="flex items-center gap-1.5">
            <span v-if="groupLabel(row, showFallbackOrigin)" class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200">
              {{ groupLabel(row, showFallbackOrigin) }}
            </span>
            <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
            <span
              v-if="crossGroup(row, showFallbackOrigin)"
              data-testid="usage-fallback-marker"
              class="text-xs text-amber-700 dark:text-amber-300"
              :title="t('admin.resilience.usage.fallbackTitle')"
            >
              {{ t('admin.resilience.usage.fallback', { group: row.group?.name || `#${row.group_id}` }) }}
            </span>
          </div>
        </template>

        <template #cell-stream="{ row }">
          <div class="flex items-center gap-1">
            <span data-testid="request-type-badge" class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getRequestTypeBadgeClass(row)">
              {{ requestTypeLabel(row, t) }}
            </span>
            <span
              v-if="row.native_compaction_v2"
              data-testid="native-compaction-badge"
              class="inline-flex items-center rounded bg-teal-100 px-2 py-0.5 text-xs font-medium text-teal-800 dark:bg-teal-900 dark:text-teal-200"
            >
              {{ t('usage.nativeCompactionV2') }}
            </span>
          </div>
        </template>

        <template #cell-billing_mode="{ row }">
          <span class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getBillingModeBadgeClass(getDisplayBillingMode(row))">
            {{ getBillingModeLabel(getDisplayBillingMode(row), t) }}
          </span>
        </template>

        <!-- 一行输入 / 输出；缓存、缓存读取占比、媒体 Token 在悬停和明细里 -->
        <template #cell-tokens="{ row }">
          <!-- 无账单请求未观测到用量：不编造 0 -->
          <span v-if="isUnbilled(row) && row.input_tokens == null && row.output_tokens == null" class="text-sm text-gray-400 dark:text-gray-500">—</span>
          <!-- 图片生成请求（仅按次计费时显示图片格式） -->
          <div v-else-if="isImageUsage(row)" class="flex items-center justify-end gap-1.5 whitespace-nowrap text-sm">
            <svg class="h-4 w-4 text-indigo-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
            </svg>
            <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ row.image_count }}{{ t('usage.imageUnit') }}</span>
            <span class="text-gray-500 dark:text-gray-400">({{ formatImageBillingSize(row, t) }})</span>
          </div>
          <!-- Token 请求 -->
          <div v-else class="flex items-center justify-end gap-2 whitespace-nowrap text-sm" data-testid="usage-token-summary">
            <span class="inline-flex items-center gap-1" :title="t('admin.usage.inputTokens')">
              <Icon name="arrowDown" size="sm" class="h-3.5 w-3.5 text-emerald-500" />
              <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ row.input_tokens?.toLocaleString() || 0 }}</span>
            </span>
            <span class="inline-flex items-center gap-1" :title="t('admin.usage.outputTokens')">
              <Icon name="arrowUp" size="sm" class="h-3.5 w-3.5 text-violet-500" />
              <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ row.output_tokens?.toLocaleString() || 0 }}</span>
            </span>
            <!-- Token Detail Tooltip（账单明细只有 usage 行才有） -->
            <div
              v-if="!isUnbilled(row)"
              class="group relative"
              @mouseenter="showTokenTooltip($event, row)"
              @mouseleave="hideTokenTooltip"
            >
              <div class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50">
                <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
              </div>
            </div>
          </div>
        </template>

        <!-- 一行实付（6 位小数）；管理端同一行跟着账号计费“A $…”；细项在悬停和明细里 -->
        <template #cell-cost="{ row }">
          <!-- 无账单请求没有费用：显示 —，失败 / 取消的标上结果 -->
          <div v-if="isUnbilled(row) && row.actual_cost == null" class="text-sm text-gray-400 dark:text-gray-500" data-testid="usage-unbilled">
            —<span v-if="unbilledWord(row, t)" class="ml-1.5 text-xs">{{ unbilledWord(row, t) }}</span>
          </div>
          <div v-else class="flex items-center justify-end gap-1.5 whitespace-nowrap text-sm">
            <span class="font-medium tabular-nums text-green-600 dark:text-green-400">{{ formatUsd(row.actual_cost, 6) }}</span>
            <span
              v-if="row.long_context_billing_applied"
              data-testid="long-context-billing-marker"
              :title="t('admin.usage.longContextPricingTooltip')"
              class="inline-flex items-center rounded px-1 py-px text-xs font-semibold leading-tight bg-amber-100 text-amber-700 ring-1 ring-inset ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30"
            >{{ t('admin.usage.longContext') }}</span>
            <span v-if="showAccountBilling && row.account_rate_multiplier != null" data-testid="usage-account-billed" class="text-xs tabular-nums text-orange-500 dark:text-orange-400">
              A {{ formatUsd(accountBilled(row), 6) }}
            </span>
            <!-- Cost Detail Tooltip -->
            <div
              class="group relative"
              @mouseenter="showTooltip($event, row)"
              @mouseleave="hideTooltip"
            >
              <div class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50">
                <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
              </div>
            </div>
          </div>
        </template>

        <!--
          一行摘要。管理端“等待 X · 首字 Y”：用户等待（端到端首输出）中性色，末次首字按分档着色；用户端“首字 X · 耗时 Y”。
          色条上端按首字分档，下端按耗时分档（耗时的数值和平均 TPS 在明细里）。
        -->
        <template #cell-latency="{ row }">
          <div class="flex items-center justify-end gap-2 whitespace-nowrap" data-testid="usage-latency-summary">
            <span
              class="h-4 w-1 shrink-0 rounded-full"
              :class="latencyBarClasses(row)"
              aria-hidden="true"
            ></span>
            <span class="flex items-baseline gap-1.5">
              <template v-if="showEndToEnd">
                <span class="text-xs text-gray-500 dark:text-gray-400" :title="t('usage.latencyEndToEndHint')">{{ t('usage.latencyWaitShort') }}</span>
                <span v-if="row.end_to_end_first_output_ms != null" data-testid="usage-latency-e2e" class="text-sm font-medium tabular-nums text-gray-700 dark:text-gray-300">{{ formatLatency(row.end_to_end_first_output_ms) }}</span>
                <span v-else data-testid="usage-latency-e2e" class="cursor-help text-sm text-gray-400 dark:text-gray-500" :title="t('usage.latencyEndToEndUnknownHint')">{{ t('usage.latencyUnknown') }}</span>
                <span class="text-gray-300 dark:text-gray-600" aria-hidden="true">·</span>
                <span class="text-xs text-gray-500 dark:text-gray-400" :title="t('usage.latencyLastAttemptHint')">{{ t('usage.latencyLastAttemptShort') }}</span>
                <span v-if="row.first_token_ms != null" data-testid="usage-latency-last-attempt" class="text-sm font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</span>
                <span v-else data-testid="usage-latency-last-attempt" class="cursor-help text-sm text-gray-400 dark:text-gray-500" :title="t('usage.latencyLastAttemptUnknownHint')">{{ t('usage.latencyUnknown') }}</span>
              </template>
              <template v-else>
                <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('usage.latencyFirstToken') }}</span>
                <span v-if="row.first_token_ms != null" data-testid="usage-latency-first-token" class="text-sm font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</span>
                <span v-else data-testid="usage-latency-first-token" class="text-sm text-gray-400 dark:text-gray-500">—</span>
                <span class="text-gray-300 dark:text-gray-600" aria-hidden="true">·</span>
                <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('usage.duration') }}</span>
                <span data-testid="usage-latency-duration" class="text-sm font-medium tabular-nums" :class="row.duration_ms == null ? 'text-gray-400 dark:text-gray-500' : LATENCY_TEXT_CLASSES[durationSeverity(row.duration_ms)]">{{ formatLatency(row.duration_ms) }}</span>
              </template>
            </span>
          </div>
        </template>

        <!-- 时间（今年省略年份）+ 每行一个“明细”按钮：点按、Enter、Space 打开同一个明细对话框 -->
        <template #cell-created_at="{ row, value }">
          <div class="flex items-center gap-3 whitespace-nowrap">
            <span class="text-sm tabular-nums text-gray-600 dark:text-gray-400">{{ formatLogTime(value) }}</span>
            <button
              type="button"
              data-testid="usage-detail-open"
              class="relative rounded text-xs font-medium text-primary-600 underline decoration-dashed underline-offset-2 transition-colors before:absolute before:-inset-x-2 before:-inset-y-3 before:content-[''] hover:text-primary-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
              :aria-label="t('usage.requestDetail.open', { time: formatLogTime(value) })"
              aria-haspopup="dialog"
              @click="detailRow = row"
            >{{ t('usage.requestDetail.button') }}</button>
          </div>
        </template>

        <template #cell-request_id="{ row }">
          <div v-if="row.request_id" class="flex max-w-[160px] items-center gap-1.5">
            <span class="truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="row.request_id">
              {{ row.request_id }}
            </span>
            <button
              type="button"
              class="shrink-0 rounded p-0.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
              :class="copiedRequestId === row.request_id ? 'text-green-500 hover:text-green-500' : ''"
              :title="copiedRequestId === row.request_id ? t('keys.copied') : t('keys.copyToClipboard')"
              @click="copyRequestId(row.request_id)"
            >
              <Icon :name="copiedRequestId === row.request_id ? 'check' : 'copy'" size="sm" class="h-3.5 w-3.5" />
            </button>
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
        </template>

        <template #cell-upstream_request_id="{ row }">
          <div v-if="row.upstream_request_id" class="flex max-w-[160px] items-center gap-1.5">
            <span class="truncate font-mono text-xs text-gray-500 dark:text-gray-400" :title="row.upstream_request_id">
              {{ row.upstream_request_id }}
            </span>
            <button
              type="button"
              class="shrink-0 rounded p-0.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
              :class="copiedRequestId === row.upstream_request_id ? 'text-green-500 hover:text-green-500' : ''"
              :title="copiedRequestId === row.upstream_request_id ? t('keys.copied') : t('keys.copyToClipboard')"
              @click="copyUpstreamRequestId(row.upstream_request_id)"
            >
              <Icon :name="copiedRequestId === row.upstream_request_id ? 'check' : 'copy'" size="sm" class="h-3.5 w-3.5" />
            </button>
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
        </template>

        <template #cell-user_agent="{ row }">
          <span v-if="row.user_agent" class="text-sm text-gray-600 dark:text-gray-400 block max-w-[320px] truncate" :title="row.user_agent">{{ row.user_agent }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
        </template>

        <!-- IP 和归属地查询同一行 -->
        <template #cell-ip_address="{ row }">
          <div v-if="row.ip_address" class="flex items-center gap-2">
            <span class="text-sm font-mono text-gray-600 dark:text-gray-400">{{ row.ip_address }}</span>
            <IpGeoCell :ip="row.ip_address" class="!mt-0" />
          </div>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
        </template>

        <template #empty><EmptyState :message="t('usage.noRecords')" /></template>
      </DataTable>
    </div>
  </div>

  <!-- Token Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tokenTooltipVisible"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{
        left: tokenTooltipPosition.x + 'px',
        top: tokenTooltipPosition.y + 'px'
      }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <UsageTokenBreakdown v-if="tokenTooltipData" :row="tokenTooltipData" :show-cache-hit-rate="props.showCacheHitRate" />
        <div class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
      </div>
    </div>
  </Teleport>

  <!-- Cost Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tooltipVisible"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{
        left: tooltipPosition.x + 'px',
        top: tooltipPosition.y + 'px'
      }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <UsageCostBreakdown v-if="tooltipData" :row="tooltipData" :show-account-billing="showAccountBilling" />
        <div class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
      </div>
    </div>
  </Teleport>

  <UsageRequestDetailDialog
    :row="detailRow"
    :show-end-to-end="props.showEndToEnd"
    :show-token-speed="props.showTokenSpeed"
    :show-cache-hit-rate="props.showCacheHitRate"
    :show-account-billing="showAccountBilling"
    :show-upstream-endpoint="showUpstreamEndpoint"
    :show-fallback-origin="props.showFallbackOrigin"
    @close="detailRow = null"
  />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { formatLogTime, formatReasoningEffort, formatUsd } from '@/utils/format'
import {
  LATENCY_BAR_CLASSES,
  LATENCY_BAR_FROM_CLASSES,
  LATENCY_BAR_TO_CLASSES,
  LATENCY_TEXT_CLASSES,
  durationSeverity,
  firstTokenSeverity,
} from '@/utils/latencyHealth'
import {
  getBillingModeLabel,
  getBillingModeBadgeClass,
  isImageUsage,
  getDisplayBillingMode,
} from '@/utils/billingMode'
import { formatImageBillingSize } from '@/utils/imageUsage'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import IpGeoCell from '@/components/common/IpGeoCell.vue'
import Icon from '@/components/icons/Icon.vue'
import UsageTokenBreakdown from './UsageTokenBreakdown.vue'
import UsageCostBreakdown from './UsageCostBreakdown.vue'
import UsageRequestDetailDialog from './UsageRequestDetailDialog.vue'
import {
  accountBilled,
  crossGroup,
  endpointPair,
  formatLatency,
  groupLabel,
  hasModelAudit,
  hasReasoningEffortMapping,
  isLikelyModelVariant,
  isUnbilled,
  modelAuditTitle,
  modelSteps,
  requestTypeLabel,
  unbilledWord,
} from './usageRow'
import { fetchBatch, getEntry } from '@/utils/ipGeoLookup'
import type { AdminUsageLog } from '@/types'
import type { Column } from '@/components/common/types'

interface Props {
  data: AdminUsageLog[]
  loading?: boolean
  columns: Column[]
  serverSideSort?: boolean
  defaultSortKey?: string
  defaultSortOrder?: 'asc' | 'desc'
  showCacheHitRate?: boolean
  showTokenSpeed?: boolean
  showAccountBilling?: boolean
  showUpstreamEndpoint?: boolean
  /** 无号兜底只是内部路由：只有管理端显示“兜底：原分组 → 实际分组”，用户端只看到原分组 */
  showFallbackOrigin?: boolean
  /** 管理端：延迟的主值是端到端首输出，最后一次尝试的首字是辅助的一行（用户端接口没有端到端，仍显示首字） */
  showEndToEnd?: boolean
  /** 嵌入统一卡片内使用：去掉自身卡片外观 */
  flat?: boolean
  /** Passed to DataTable's rowClass (a stable function from the page's setup). */
  rowClass?: (row: AdminUsageLog, index: number) => unknown
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  serverSideSort: false,
  defaultSortKey: '',
  defaultSortOrder: 'asc',
  showCacheHitRate: false,
  showTokenSpeed: false,
  showAccountBilling: true,
  showUpstreamEndpoint: true,
  showFallbackOrigin: false,
  showEndToEnd: false,
  flat: false
})
const emit = defineEmits<{
  userClick: [userID: number, email?: string]
  sort: [key: string, order: 'asc' | 'desc']
  ipGeoBatchFailed: []
}>()
const { t } = useI18n()

const appStore = useAppStore()
const copiedRequestId = ref<string | null>(null)
const showAccountBilling = props.showAccountBilling
const showUpstreamEndpoint = props.showUpstreamEndpoint
const showFallbackOrigin = computed(() => props.showFallbackOrigin)
const ipGeoBatchLoading = ref(false)
/** The row whose request detail dialog is open (a snapshot: a live refresh does not change it under the reader). */
const detailRow = ref<AdminUsageLog | null>(null)

// 后端 0.6.5 起，管理端列表把没有账单的请求（多为失败或取消）和 usage 行排在一起。这些行来自另一张表：
// id 自成序列，没有费用，常常也没有 Token 数。行键带上来源；费用与 Token 显示 —，不编造 0。
const tableRef = ref<InstanceType<typeof DataTable> | null>(null)
/**
 * The list's first row is in view (below the sticky page header, spec 8.5a) and the table has not been scrolled inside
 * itself: new rows may come in at the top (the live engine's log mode follows the server only then).
 */
const isAtTop = () => tableRef.value?.isAtTop() ?? true
const scrollToTop = () => tableRef.value?.scrollToTop()
defineExpose({ isAtTop, scrollToTop })

const rowKey = (row: AdminUsageLog) => `${row.source ?? 'usage'}:${row.id}`

const showIpGeoToolbar = computed(() => props.columns.some((col) => col.key === 'ip_address'))

/** The upstream endpoint is shown on this page, known and not the inbound one. */
const upstreamDiffers = (row: AdminUsageLog) => {
  const { inbound, upstream } = endpointPair(row, showUpstreamEndpoint)
  return !!upstream && upstream !== inbound
}
const endpointTitle = (row: AdminUsageLog) => {
  const { inbound, upstream } = endpointPair(row, showUpstreamEndpoint)
  const lines = [`${t('usage.inboundEndpoint')}: ${inbound || '—'}`]
  if (upstream != null) lines.push(`${t('usage.upstreamEndpoint')}: ${upstream || '—'}`)
  return lines.join('\n')
}

const currentPageIps = computed(() =>
  Array.from(new Set(props.data.map((row) => row.ip_address).filter((ip): ip is string => Boolean(ip))))
)

const pendingIpCount = computed(() => {
  if (!showIpGeoToolbar.value) return 0
  return currentPageIps.value.filter((ip) => {
    const status = getEntry(ip).status
    return status === 'idle' || status === 'error'
  }).length
})

const handleBatchFetchIpGeo = async () => {
  ipGeoBatchLoading.value = true
  try {
    const ok = await fetchBatch(currentPageIps.value)
    if (!ok) emit('ipGeoBatchFailed')
  } finally {
    ipGeoBatchLoading.value = false
  }
}

const copyIdentifier = async (value: string, copiedMessage: string) => {
  try {
    await navigator.clipboard.writeText(value)
    copiedRequestId.value = value
    appStore.showSuccess(copiedMessage)
    window.setTimeout(() => {
      if (copiedRequestId.value === value) copiedRequestId.value = null
    }, 2000)
  } catch {
    appStore.showError(t('common.copyFailed'))
  }
}

const copyRequestId = (requestId: string) => copyIdentifier(requestId, t('admin.usage.requestIdCopied'))
const copyUpstreamRequestId = (upstreamRequestId: string) =>
  copyIdentifier(upstreamRequestId, t('admin.usage.upstreamRequestIdCopied'))

// Tooltip state - cost
const tooltipVisible = ref(false)
const tooltipPosition = ref({ x: 0, y: 0 })
const tooltipData = ref<AdminUsageLog | null>(null)

// Tooltip state - token
const tokenTooltipVisible = ref(false)
const tokenTooltipPosition = ref({ x: 0, y: 0 })
const tokenTooltipData = ref<AdminUsageLog | null>(null)

const getRequestTypeBadgeClass = (row: AdminUsageLog): string => {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
  if (requestType === 'live') return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200'
  if (requestType === 'ws_v2') return 'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200'
  if (requestType === 'stream') return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
  if (requestType === 'sync') return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
}

function latencyBarClasses(row: AdminUsageLog) {
  const duration = row.duration_ms == null ? null : durationSeverity(row.duration_ms)
  if (row.first_token_ms == null) {
    return duration == null ? 'bg-gray-400 dark:bg-gray-500' : LATENCY_BAR_CLASSES[duration]
  }
  return [
    'bg-gradient-to-b from-40% to-60%',
    LATENCY_BAR_FROM_CLASSES[firstTokenSeverity(row.first_token_ms)],
    duration == null ? 'to-gray-400 dark:to-gray-500' : LATENCY_BAR_TO_CLASSES[duration],
  ]
}

// Cost tooltip functions
const showTooltip = (event: MouseEvent, row: AdminUsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()
  tooltipData.value = row
  tooltipPosition.value.x = rect.right + 8
  tooltipPosition.value.y = rect.top + rect.height / 2
  tooltipVisible.value = true
}

const hideTooltip = () => {
  tooltipVisible.value = false
  tooltipData.value = null
}

// Token tooltip functions
const showTokenTooltip = (event: MouseEvent, row: AdminUsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()
  tokenTooltipData.value = row
  tokenTooltipPosition.value.x = rect.right + 8
  tokenTooltipPosition.value.y = rect.top + rect.height / 2
  tokenTooltipVisible.value = true
}

const hideTokenTooltip = () => {
  tokenTooltipVisible.value = false
  tokenTooltipData.value = null
}
</script>
