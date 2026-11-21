<template>
  <div :class="flat ? '' : 'card overflow-hidden max-md:-mx-4 max-md:rounded-none max-md:border-x-0'">
    <!-- On touch the batch button is 40px high and the row loses its padding, so the row keeps its 40px (as the errors
         tables' IpGeoBatchToolbar). -->
    <div
      v-if="showIpGeoToolbar"
      class="flex items-center justify-end gap-2 border-b border-gray-200 px-4 py-2 dark:border-dark-700 [@media(pointer:coarse)]:py-0"
      data-testid="usage-ip-geo-toolbar"
    >
      <span v-if="pendingIpCount > 0" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('usage.ipGeo.pending', { count: pendingIpCount }) }}
      </span>
      <button
        type="button"
        data-testid="usage-ip-geo-batch"
        class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-primary-600 [@media(pointer:coarse)]:min-h-10 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:bg-primary-900/30"
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
        :columns="tableColumns"
        :data="data"
        :row-key="rowKey"
        :row-class="rowClass"
        :loading="loading"
        :server-side-sort="serverSideSort"
        :sort="sort"
        :scroll-label="scrollLabel"
        @sort="(key, order) => $emit('sort', key, order)"
      >
        <!--
          手机上第一列冻结为身份列：一行邮箱，超长省略，点邮箱照旧看充值记录（伪元素把可点区域扩到 40px 高）；#编号和“已删除”在明细里。
          从 768px 起编号和“已删除”跟在邮箱后面；768–1023px（平板上也冻结）最宽 240px，邮箱超长省略、悬停看全文；
          从 1024px 起不设上限，与改版前一样整段显示。
        -->
        <template #cell-user="{ row }">
          <template v-if="identityKey === 'user'">
            <button
              v-if="row.user?.email"
              type="button"
              data-testid="usage-identity-user"
              class="relative block w-full text-left text-sm font-medium text-primary-600 before:absolute before:inset-x-0 before:-inset-y-3 before:content-[''] dark:text-primary-400"
              :title="row.user.email"
              @click="$emit('userClick', row.user_id, row.user?.email)"
            ><span class="block truncate underline decoration-dashed underline-offset-2">{{ row.user.email }}</span></button>
            <CellEmpty v-else />
          </template>
          <div v-else class="flex items-center text-sm max-lg:max-w-[240px]" data-testid="usage-user-cell">
            <button
              v-if="row.user?.email"
              type="button"
              data-testid="usage-user-email"
              class="relative min-w-0 text-left font-medium text-primary-600 transition-colors before:absolute before:inset-x-0 before:-inset-y-3 before:content-[''] hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              @click="$emit('userClick', row.user_id, row.user?.email)"
              :title="`${row.user.email}\n${t('admin.usage.clickToViewBalance')}`"
            >
              <span class="block truncate underline decoration-dashed underline-offset-2">{{ row.user.email }}</span>
            </button>
            <CellEmpty v-else />
            <span v-if="row.user?.deleted_at" class="ml-1 inline-flex flex-none items-center rounded px-1 py-px text-xs font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30">
              {{ t('admin.usage.userDeletedBadge') }}
            </span>
            <span class="ml-1 flex-none tabular-nums text-gray-500 dark:text-gray-400">#{{ row.user_id }}</span>
          </div>
        </template>

        <template #cell-api_key="{ row }">
          <TableIdentityCell v-if="identityKey === 'api_key' && row.api_key?.name" :primary="row.api_key.name" />
          <span v-else-if="row.api_key?.name" class="block truncate text-sm text-gray-900 dark:text-white max-lg:max-w-[240px]" :title="row.api_key.name">{{ row.api_key.name }}</span>
          <CellEmpty v-else />
        </template>

        <template #cell-account="{ row }">
          <span class="inline-flex max-w-[280px] items-baseline align-middle">
            <span v-if="row.account?.name" class="min-w-0 truncate text-sm text-gray-900 dark:text-white" :title="row.account.name">{{ row.account.name }}</span>
            <CellEmpty v-else />
            <!-- 后端 0.6.5 请求层：换号后成功的请求标"已救回"，换过号的标次数；与账号同一行 -->
            <span v-if="row.result === 2 || (row.switches ?? 0) > 0" class="ml-1.5 inline-flex flex-none gap-x-1.5 text-xs" data-testid="usage-request-marks">
              <span v-if="row.result === 2" class="text-amber-600 dark:text-amber-400">{{ t('admin.usage.resultRescued') }}</span>
              <span v-if="(row.switches ?? 0) > 0" class="text-gray-500 dark:text-gray-400">{{ t('admin.usage.switches', { n: row.switches }) }}</span>
            </span>
          </span>
        </template>

        <!--
          照改版前：12px，请求的模型一行，管理端发往上游的模型在下面用“↳”逐级缩进（映射链每级多缩一格），上游答回的模型不一致属
          少见的异常，再另起一行标出。用户端只写请求的模型（不推断、不显示上游映射），明细同样。
        -->
        <template #cell-model="{ row }">
          <div class="space-y-0.5 text-xs" data-testid="usage-model-cell">
            <div v-if="shownModelSteps(row).length > 1" class="space-y-0.5" data-testid="usage-model-steps">
              <div
                v-for="(step, i) in shownModelSteps(row)"
                :key="i"
                class="break-all"
                :class="i === 0 ? 'font-medium text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'"
                :style="i > 0 && isMappingChain(row) ? `padding-left: ${i * 0.75}rem` : ''"
              ><span v-if="i > 0" class="mr-0.5">↳</span>{{ step }}</div>
            </div>
            <span v-else class="font-medium text-gray-900 dark:text-white" data-testid="usage-model-steps">{{ row.model }}</span>
            <div
              v-if="isAdmin && hasModelAudit(row)"
              class="break-all pl-3 text-[11px]"
              :class="isLikelyModelVariant(row) ? 'text-amber-600 dark:text-amber-400' : 'text-orange-600 dark:text-orange-400'"
              :title="modelAuditTitle(row, t)"
            >
              <span class="mr-1">↳ {{ t('usage.upstreamResponseModel') }}:</span>{{ row.upstream_response_model }}
              <span
                class="ml-1 inline-flex rounded px-1 py-px text-[10px] font-medium ring-1 ring-inset"
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
            <span class="text-gray-900 dark:text-white" :class="{ 'font-medium': isAdmin && hasReasoningEffortMapping(row) }">{{ formatReasoningEffort(row.reasoning_effort) }}</span>
            <template v-if="isAdmin && hasReasoningEffortMapping(row)">
              <span class="mx-1 text-gray-400 dark:text-gray-500" aria-hidden="true">→</span>
              <span class="text-gray-500 dark:text-gray-400">{{ formatReasoningEffort(row.upstream_reasoning_effort) }}</span>
            </template>
          </span>
        </template>

        <!-- 两行“入站: … / 上游: …”（上游只有管理端有） -->
        <template #cell-endpoint="{ row }">
          <div class="max-w-[320px] space-y-1 text-xs" data-testid="usage-endpoint">
            <div class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.inbound') }}:</span>
              <span class="ml-1">{{ row.inbound_endpoint?.trim() || '—' }}</span>
            </div>
            <div v-if="showUpstreamEndpoint" class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.upstream') }}:</span>
              <span class="ml-1">{{ row.upstream_endpoint?.trim() || '—' }}</span>
            </div>
          </div>
        </template>

        <template #cell-group="{ row }">
          <div class="flex items-center gap-1.5">
            <span v-if="groupLabel(row, showFallbackOrigin)" class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200">
              {{ groupLabel(row, showFallbackOrigin) }}
            </span>
            <CellEmpty v-else />
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

        <!-- 输入 / 输出一行，缓存读写、缓存读取占比、图片 Token 各占一行；右侧小图标悬停看 Token 明细 -->
        <template #cell-tokens="{ row }">
          <!-- 无账单请求未观测到用量：不编造 0 -->
          <span v-if="isUnbilled(row) && row.input_tokens == null && row.output_tokens == null" class="text-sm text-gray-400 dark:text-gray-500">—</span>
          <!-- 图片生成请求（仅按次计费时显示图片格式） -->
          <div v-else-if="isImageUsage(row)" class="flex items-center gap-1.5">
            <svg class="h-4 w-4 text-indigo-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
            </svg>
            <span class="font-medium text-gray-900 dark:text-white">{{ row.image_count }}{{ t('usage.imageUnit') }}</span>
            <span class="text-gray-400">({{ formatImageBillingSize(row, t) }})</span>
          </div>
          <!-- Token 请求 -->
          <div v-else class="flex items-center gap-1.5" data-testid="usage-token-cell">
            <div class="space-y-1 text-sm">
              <div class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowDown" size="sm" class="h-3.5 w-3.5 text-emerald-500" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.input_tokens?.toLocaleString() || 0 }}</span>
                </div>
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowUp" size="sm" class="h-3.5 w-3.5 text-violet-500" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.output_tokens?.toLocaleString() || 0 }}</span>
                </div>
              </div>
              <div v-if="row.cache_read_tokens > 0 || row.cache_creation_tokens > 0" class="flex items-center gap-2">
                <div v-if="row.cache_read_tokens > 0" class="inline-flex items-center gap-1" data-testid="usage-cache-read">
                  <svg class="h-3.5 w-3.5 text-sky-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4" /></svg>
                  <span class="font-medium text-sky-600 dark:text-sky-400">{{ formatCacheTokens(row.cache_read_tokens) }}</span>
                </div>
                <div v-if="row.cache_creation_tokens > 0" class="inline-flex items-center gap-1" data-testid="usage-cache-creation">
                  <svg class="h-3.5 w-3.5 text-amber-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" /></svg>
                  <span class="font-medium text-amber-600 dark:text-amber-400">{{ formatCacheTokens(row.cache_creation_tokens) }}</span>
                  <span v-if="row.cache_creation_1h_tokens > 0" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-orange-100 text-orange-600 ring-1 ring-inset ring-orange-200 dark:bg-orange-500/20 dark:text-orange-400 dark:ring-orange-500/30">1h</span>
                  <span v-if="row.cache_ttl_overridden" :title="t('usage.cacheTtlOverriddenHint')" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30 cursor-help">{{ t('usage.cacheTtlOverriddenLabel') }}</span>
                </div>
              </div>
              <div v-if="props.showCacheHitRate && hasUsageTokenMetrics(row)" class="flex items-center gap-2 text-xs" :title="t('usage.cacheHitRateHint')">
                <span data-testid="usage-cache-hit-rate" :aria-label="`${t('usage.cacheHitRate')}: ${formatCacheHitRate(row)}`" class="font-medium tabular-nums text-sky-600 dark:text-sky-400">{{ formatCacheHitRate(row) }}</span>
              </div>
              <div v-if="hasImageInputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-fuchsia-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-fuchsia-600 dark:text-fuchsia-400">{{ row.image_input_tokens.toLocaleString() }}</span>
                </div>
              </div>
              <div v-if="hasImageOutputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-pink-500" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-pink-600 dark:text-pink-400">{{ row.image_output_tokens.toLocaleString() }}</span>
                </div>
              </div>
            </div>
            <!-- Token Detail Tooltip（账单明细只有 usage 行才有；触屏没有悬停，明细在“明细”里） -->
            <div
              v-if="!isUnbilled(row)"
              class="group relative [@media(hover:none)]:hidden"
              data-testid="usage-token-tip"
              @mouseenter="showTokenTooltip($event, row)"
              @mouseleave="hideTokenTooltip"
            >
              <div class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50">
                <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
              </div>
            </div>
          </div>
        </template>

        <!--
          第一行实付（长上下文标记）和 16px 小图标：悬停看费用明细，点击开档位明细；主指针是触屏时可点区放大到 40px，图标不变。
          第二行管理端的账号计费“A $…”。
        -->
        <template #cell-cost="{ row }">
          <!-- 无账单请求没有费用：显示 —，失败 / 取消的标上结果 -->
          <div v-if="isUnbilled(row) && row.actual_cost == null" class="text-sm text-gray-400 dark:text-gray-500" data-testid="usage-unbilled">
            —<span v-if="unbilledWord(row, t)" class="ml-1.5 text-xs">{{ unbilledWord(row, t) }}</span>
          </div>
          <div v-else class="text-sm">
            <div class="flex items-center gap-1.5">
              <span class="font-medium text-green-600 dark:text-green-400">{{ formatUsd(row.actual_cost, 6) }}</span>
              <span
                v-if="row.long_context_billing_applied"
                data-testid="long-context-billing-marker"
                :title="t('admin.usage.longContextPricingTooltip')"
                class="inline-flex items-center rounded px-1 py-px text-[10px] font-semibold leading-tight bg-amber-100 text-amber-700 ring-1 ring-inset ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30"
              >{{ t('admin.usage.longContext') }}</span>
              <button
                type="button"
                class="group relative flex h-4 w-4 cursor-pointer items-center justify-center rounded-full bg-gray-100 transition-colors hover:bg-blue-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:bg-gray-700 dark:hover:bg-blue-900/50 [@media(pointer:coarse)]:before:absolute [@media(pointer:coarse)]:before:-inset-3 [@media(pointer:coarse)]:before:content-['']"
                :aria-label="t('usage.tierDetails.title')"
                aria-haspopup="dialog"
                data-testid="usage-tier-details"
                @mouseenter="showTooltip($event, row)"
                @mouseleave="hideTooltip"
                @click="hideTooltip(); tierDetailRow = row"
              >
                <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
              </button>
            </div>
            <div v-if="showAccountBilling && row.account_rate_multiplier != null" data-testid="usage-account-billed" class="mt-0.5 text-[11px] text-orange-500 dark:text-orange-400">
              A {{ formatUsd(accountBilled(row), 6) }}
            </div>
          </div>
        </template>

        <!--
          左侧色条上端按首字分档，下端按耗时分档。管理端四行：等待（端到端首输出，灰色）、首字、耗时（末次尝试，分档着色）、
          TPS（不着色），标签悬停说明口径；用户端：首字、总耗时、平均 TPS。
        -->
        <template #cell-latency="{ row }">
          <div class="flex items-stretch gap-2" data-testid="usage-latency-cell">
            <span
              class="w-1 shrink-0 rounded-full"
              :class="latencyBarClasses(row)"
              aria-hidden="true"
            ></span>
            <div class="grid grid-cols-[max-content_max-content] items-baseline gap-x-2 gap-y-0.5 text-xs">
              <template v-if="showEndToEnd">
                <span class="text-gray-500 dark:text-gray-400" :title="t('usage.latencyEndToEndHint')">{{ t('usage.latencyWaitShort') }}</span>
                <span v-if="row.end_to_end_first_output_ms != null" data-testid="usage-latency-e2e" class="tabular-nums text-gray-500 dark:text-gray-400">{{ formatLatency(row.end_to_end_first_output_ms) }}</span>
                <span v-else data-testid="usage-latency-e2e" class="cursor-help text-gray-400 dark:text-gray-500" :title="t('usage.latencyEndToEndUnknownHint')">{{ t('usage.latencyUnknown') }}</span>
                <span class="text-gray-400 dark:text-gray-500" :title="t('usage.latencyLastAttemptHint')">{{ t('usage.latencyFirstToken') }}</span>
                <span v-if="row.first_token_ms != null" data-testid="usage-latency-last-attempt" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</span>
                <span v-else data-testid="usage-latency-last-attempt" class="cursor-help text-gray-400 dark:text-gray-500" :title="t('usage.latencyLastAttemptUnknownHint')">{{ t('usage.latencyUnknown') }}</span>
              </template>
              <template v-else>
                <span class="text-gray-400 dark:text-gray-500">{{ t('usage.latencyFirstToken') }}</span>
                <span v-if="row.first_token_ms != null" data-testid="usage-latency-first-token" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</span>
                <span v-else data-testid="usage-latency-first-token" class="text-gray-400 dark:text-gray-500">—</span>
              </template>
              <span class="text-gray-400 dark:text-gray-500" :title="showEndToEnd ? t('usage.latencyLastAttemptDurationHint') : undefined">{{ showEndToEnd ? t('usage.duration') : t('usage.latencyDuration') }}</span>
              <span data-testid="usage-latency-duration" class="font-medium tabular-nums" :class="row.duration_ms == null ? 'text-gray-400 dark:text-gray-500' : LATENCY_TEXT_CLASSES[durationSeverity(row.duration_ms)]">{{ formatLatency(row.duration_ms) }}</span>
              <template v-if="props.showTokenSpeed && hasUsageTokenMetrics(row)">
                <span class="text-gray-500 dark:text-gray-400" :title="showEndToEnd ? t('usage.outputTpsHint') : undefined">{{ showEndToEnd ? t('usage.outputTpsShort') : t('usage.outputTps') }}</span>
                <span data-testid="usage-token-speed" class="whitespace-nowrap font-medium tabular-nums text-gray-700 dark:text-gray-300" :title="showEndToEnd ? t('usage.outputTpsHint') : undefined">{{ formatTokenSpeed(row) }}</span>
              </template>
            </div>
          </div>
        </template>

        <!--
          时间（今年省略年份）。1024 以下“明细”在右侧冻结的 44px 列里（与账号页“⋯”同一做法），不用横滑到最后一列；从 1024px 起
          主指针是触屏（没有悬停）时时间旁一个“明细”按钮。鼠标为主的电脑（带触摸屏的也算）和改版前一样没有“明细”：四列的细项都在
          格子和悬停里，档位明细从费用的小图标点开。点按、Enter、Space 都打开同一个明细对话框。
        -->
        <template #cell-created_at="{ row, value }">
          <div class="flex items-center gap-3 whitespace-nowrap">
            <span class="text-sm tabular-nums text-gray-600 dark:text-gray-400">{{ formatListDateTime(value) }}</span>
            <button
              v-if="desktop && !mouseFirst"
              type="button"
              data-testid="usage-detail-open"
              :data-row-key="rowKey(row)"
              class="relative rounded text-xs font-medium text-primary-600 underline decoration-dashed underline-offset-2 transition-colors before:absolute before:-inset-x-2 before:-inset-y-3 before:content-[''] hover:text-primary-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
              :aria-label="t('usage.requestDetail.open', { time: formatListDateTime(value) })"
              aria-haspopup="dialog"
              @click="detailRow = row"
            >{{ t('usage.requestDetail.button') }}</button>
          </div>
        </template>

        <!-- The 44px column's header is the buttons' icon, named for screen readers by aria-label (no clipped text). -->
        <template #header-actions>
          <span role="img" class="inline-flex w-full justify-center text-gray-400 dark:text-gray-500" :aria-label="t('usage.requestDetail.button')" data-testid="usage-detail-header">
            <Icon name="document" size="sm" aria-hidden="true" />
          </span>
        </template>
        <template #cell-actions="{ row }">
          <!-- A 40px button in a block whose negative margins give back 20px: the row stays one text line high. -->
          <div class="-my-2.5 flex justify-center">
            <button
              type="button"
              data-testid="usage-detail-open"
              :data-row-key="rowKey(row)"
              class="inline-flex h-10 w-10 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              :aria-label="t('usage.requestDetail.open', { time: formatListDateTime(row.created_at) })"
              :title="t('usage.requestDetail.button')"
              aria-haspopup="dialog"
              @click="detailRow = row"
            >
              <Icon name="document" size="sm" />
            </button>
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
          <CellEmpty v-else />
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
          <CellEmpty v-else />
        </template>

        <template #cell-user_agent="{ row }">
          <span v-if="row.user_agent" class="text-sm text-gray-600 dark:text-gray-400 block max-w-[320px] truncate" :title="row.user_agent">{{ row.user_agent }}</span>
          <CellEmpty v-else />
        </template>

        <!-- IP 和归属地查询同一行 -->
        <template #cell-ip_address="{ row }">
          <div v-if="row.ip_address" class="flex items-center gap-2">
            <span class="text-sm font-mono text-gray-600 dark:text-gray-400">{{ row.ip_address }}</span>
            <IpGeoCell :ip="row.ip_address" class="!mt-0" />
          </div>
          <CellEmpty v-else />
        </template>

        <template #empty><EmptyState :message="t('usage.noRecords')" /></template>
      </DataTable>
    </div>
  </div>

  <UsageTierDetailsDialog v-if="tierDetailRow" :row="tierDetailRow" :admin="isAdmin" @close="tierDetailRow = null" />

  <!-- Token Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tokenTooltipVisible"
      ref="tokenTipEl"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{ left: tokenTip.x + 'px', top: tokenTip.y + 'px' }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <UsageTokenBreakdown v-if="tokenTooltipData" :row="tokenTooltipData" />
        <div v-if="tokenTip.side === 'right'" class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
        <div v-else class="absolute left-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-l-[6px] border-t-[6px] border-b-transparent border-l-gray-900 border-t-transparent dark:border-l-gray-800"></div>
      </div>
    </div>
  </Teleport>

  <!-- Cost Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tooltipVisible"
      ref="costTipEl"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{ left: costTip.x + 'px', top: costTip.y + 'px' }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <UsageCostBreakdown v-if="tooltipData" :row="tooltipData" :show-account-billing="showAccountBilling" />
        <div v-if="costTip.side === 'right'" class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
        <div v-else class="absolute left-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-l-[6px] border-t-[6px] border-b-transparent border-l-gray-900 border-t-transparent dark:border-l-gray-800"></div>
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
    :show-admin-fields="isAdmin"
    @open-tier-details="tierDetailRow = $event"
    @close="detailRow = null"
    @copy="(kind, value) => (kind === 'request_id' ? copyRequestId(value) : copyUpstreamRequestId(value))"
  />
</template>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { formatListDateTime, formatReasoningEffort, formatUsd } from '@/utils/format'
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
import { formatImageBillingSize, hasImageInputTokens, hasImageOutputTokens } from '@/utils/imageUsage'
import { formatCacheTokens } from '@/utils/formatters'
import { formatCacheHitRate, formatTokenSpeed, hasUsageTokenMetrics } from '@/utils/usageMetrics'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import IpGeoCell from '@/components/common/IpGeoCell.vue'
import CellEmpty from '@/components/common/CellEmpty.vue'
import TableIdentityCell from '@/components/common/TableIdentityCell.vue'
import { useMediaQuery } from '@vueuse/core'
import { useTableViewport } from '@/composables/useTableViewport'
import Icon from '@/components/icons/Icon.vue'
import UsageTokenBreakdown from './UsageTokenBreakdown.vue'
import UsageCostBreakdown from './UsageCostBreakdown.vue'
import UsageRequestDetailDialog from './UsageRequestDetailDialog.vue'
import UsageTierDetailsDialog from './UsageTierDetailsDialog.vue'
import {
  accountBilled,
  crossGroup,
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
  /** The sort the page requests the list with (server-side): the headers follow it, also while its column is hidden. */
  sort?: { key: string; order: 'asc' | 'desc' } | null
  /** Names the table's scroll area (a region that takes keyboard focus while it scrolls sideways). */
  scrollLabel?: string
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
  /**
   * Whose page this is. The user page shows the model and reasoning effort asked for only (never an upstream mapping or
   * the upstream's answer), in the cells, tips and detail, and its detail has no user, account or upstream request ID.
   */
  viewer?: 'admin' | 'user'
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  serverSideSort: false,
  sort: undefined,
  scrollLabel: undefined,
  showCacheHitRate: false,
  showTokenSpeed: false,
  showAccountBilling: true,
  showUpstreamEndpoint: true,
  showFallbackOrigin: false,
  showEndToEnd: false,
  flat: false,
  viewer: 'user'
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

/**
 * Below 768px the table's first column is frozen as the identity column (the shared table layout): the admin page's
 * user, the user page's API key. Its cell becomes the two-line identity cell; from 768px the cells stay as they are.
 */
const { phone, desktop } = useTableViewport()
const identityKey = computed(() => (phone.value ? props.columns[0]?.key ?? null : null))
/**
 * Below 1024px a 44px "Details" column frozen on the right (the table's actions column, as the accounts list's "⋯"), so
 * a row's detail is one tap away without scrolling to the time; from 1024px the button sits beside the time.
 */
const tableColumns = computed<Column[]>(() =>
  desktop.value
    ? props.columns
    : [...props.columns, { key: 'actions', label: t('usage.requestDetail.button'), class: 'w-11 min-w-11 !px-0 text-center' }]
)
/** A mouse-first computer (a touch screen beside the mouse included): no "Details" beside the time, as before the plan. */
const mouseFirst = useMediaQuery('(hover: hover) and (pointer: fine)')
const isAdmin = computed(() => props.viewer === 'admin')
/** The model steps a page may show: the admin page the whole mapping, the user page the model asked for. */
const shownModelSteps = (row: AdminUsageLog) => (isAdmin.value ? modelSteps(row) : [row.model])
/** The steps come from the mapping chain (each step one more indent), not just requested and upstream. */
const isMappingChain = (row: AdminUsageLog) => !!row.model_mapping_chain?.includes('→')

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

// Hover tips (desktop): to the right of their icon, or on its left when they would leave the viewport there; kept
// inside it top to bottom. Measured once they render, so a tip's real size decides.
type TipPlace = { x: number; y: number; side: 'right' | 'left' }
const TIP_GAP = 8
const placeTip = async (anchor: DOMRect, place: { value: TipPlace }, el: { value: HTMLElement | null }) => {
  place.value = { x: anchor.right + TIP_GAP, y: anchor.top + anchor.height / 2, side: 'right' }
  await nextTick()
  const box = el.value
  if (!box) return
  const width = box.offsetWidth
  const height = box.offsetHeight
  const room = window.innerWidth - TIP_GAP
  let x = anchor.right + TIP_GAP
  let side: TipPlace['side'] = 'right'
  if (x + width > room) {
    const left = anchor.left - TIP_GAP - width
    if (left >= TIP_GAP) {
      x = left
      side = 'left'
    } else {
      x = Math.max(TIP_GAP, room - width)
    }
  }
  const half = height / 2
  const y = Math.min(Math.max(anchor.top + anchor.height / 2, TIP_GAP + half), window.innerHeight - TIP_GAP - half)
  place.value = { x, y, side }
}

// Tooltip state - cost
const tierDetailRow = ref<AdminUsageLog | null>(null)
const tooltipVisible = ref(false)
const costTip = ref<TipPlace>({ x: 0, y: 0, side: 'right' })
const costTipEl = ref<HTMLElement | null>(null)
const tooltipData = ref<AdminUsageLog | null>(null)

// Tooltip state - token
const tokenTooltipVisible = ref(false)
const tokenTip = ref<TipPlace>({ x: 0, y: 0, side: 'right' })
const tokenTipEl = ref<HTMLElement | null>(null)
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
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  tooltipData.value = row
  tooltipVisible.value = true
  void placeTip(rect, costTip, costTipEl)
}

const hideTooltip = () => {
  tooltipVisible.value = false
  tooltipData.value = null
}

// Token tooltip functions
const showTokenTooltip = (event: MouseEvent, row: AdminUsageLog) => {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  tokenTooltipData.value = row
  tokenTooltipVisible.value = true
  void placeTip(rect, tokenTip, tokenTipEl)
}

const hideTokenTooltip = () => {
  tokenTooltipVisible.value = false
  tokenTooltipData.value = null
}
</script>
