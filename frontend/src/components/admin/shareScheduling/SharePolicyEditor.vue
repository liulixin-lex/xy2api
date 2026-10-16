<template>
  <div v-if="entry" class="space-y-5">
    <section class="card p-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.policy.scheduler') }}</h2>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ entry.view.version > 0 && entry.view.updated_at ? t('admin.shareScheduling.policy.updated', { version: entry.view.version, time: formatDateTime(entry.view.updated_at) }) : t('admin.shareScheduling.policy.neverSaved') }}
          </p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="share-rebalance" @click="rebalanceOpen = true">{{ t('admin.shareScheduling.rebalance.action') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="share-reload" @click="emit('reload')">{{ t('admin.shareScheduling.policy.reload') }}</button>
        </div>
      </div>
      <fieldset class="mt-4 grid gap-3 sm:grid-cols-3" :disabled="busy">
        <legend class="sr-only">{{ t('admin.shareScheduling.policy.scheduler') }}</legend>
        <label
          v-for="option in schedulers"
          :key="option"
          class="flex cursor-pointer flex-col rounded-xl border p-3 text-sm transition-colors focus-within:ring-2 focus-within:ring-primary-500"
          :class="entry.draft.policy.scheduler === option ? 'border-primary-400 bg-primary-50 dark:border-primary-700 dark:bg-primary-900/20' : 'border-gray-200 hover:border-gray-300 dark:border-dark-600 dark:hover:border-dark-500'"
        >
          <span class="flex items-center gap-2 font-medium text-gray-900 dark:text-white">
            <input v-model="entry.draft.policy.scheduler" type="radio" name="share-scheduler" :value="option" :data-testid="'share-scheduler-' + option">
            {{ labels.scheduler(option) }}
          </span>
          <span class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.schedulerHints.' + option) }}</span>
        </label>
      </fieldset>
      <p v-if="entry.view.imported_from && entry.view.scheduler === 'sub2api'" class="mt-4 rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="share-imported">
        {{ t('admin.shareScheduling.policy.importedNotice', { source: entry.view.imported_from }) }}
      </p>
      <p v-if="entry.view.invalid" class="mt-4 rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert">
        {{ t('admin.shareScheduling.policy.invalidNotice', { reason: entry.view.invalid }) }}
      </p>
      <p v-if="entry.draft.stale.length" class="mt-4 rounded-xl bg-gray-50 p-3 text-sm text-gray-700 dark:bg-dark-700 dark:text-dark-200">
        {{ t('admin.shareScheduling.policy.staleNotice', { count: entry.draft.stale.length }) }}
      </p>
    </section>

    <section class="card p-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0 max-w-3xl">
          <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.policy.members') }}</h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.policy.membersHint', { weight: t('admin.shareScheduling.policy.defaultWeights.' + entry.draft.policy.defaults.weight) }) }}</p>
        </div>
        <span class="rounded-lg px-2 py-1 text-xs font-medium" :class="shares.activeTier === null ? 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300' : 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'" data-testid="share-active-tier">
          {{ shares.activeTier === null ? t('admin.shareScheduling.policy.noActiveTier') : t('admin.shareScheduling.policy.activeTier', { tier: shares.activeTier }) }}
        </span>
      </div>

      <div class="mt-4 flex flex-wrap items-end gap-3">
        <label class="min-w-48 flex-1 text-sm text-gray-700 dark:text-dark-200">
          <span class="sr-only">{{ t('admin.shareScheduling.policy.search') }}</span>
          <input v-model.trim="search" type="search" class="input" :placeholder="t('admin.shareScheduling.policy.search')" data-testid="share-member-search">
        </label>
      </div>

      <div v-if="tiers.length" class="mt-3 flex flex-wrap gap-2" data-testid="share-tiers">
        <div
          v-for="tier in tiers"
          :key="tier.tier"
          class="min-w-44 rounded-xl border p-3 text-xs transition-colors"
          :class="[tierFilter === tier.tier ? 'border-primary-400 bg-primary-50 dark:border-primary-700 dark:bg-primary-900/20' : 'border-gray-200 dark:border-dark-600', dropTarget === tier.tier ? 'ring-2 ring-primary-400' : '']"
          :data-testid="'share-tier-card-' + tier.tier"
          @dragover.prevent="dropTarget = tier.tier"
          @dragleave="dropTarget = null"
          @drop.prevent="dropOnTier(tier.tier)"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.policy.tierCard.title', { tier: tier.tier }) }}</span>
            <span v-if="shares.activeTier === tier.tier" class="rounded bg-emerald-50 px-1.5 py-0.5 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300">{{ t('admin.shareScheduling.policy.tierCard.active') }}</span>
          </div>
          <div class="mt-1 text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.tierCard.summary', { count: tier.count, weight: tier.weight }) }}</div>
          <div class="text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.tierCard.effective', { value: formatSharePercent(tier.effective) }) }}</div>
          <div class="mt-2 flex flex-wrap gap-1">
            <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" :data-testid="'share-tier-pick-' + tier.tier" @click="pickTier(tier.tier)">{{ t('admin.shareScheduling.policy.tierCard.pick') }}</button>
            <button type="button" class="btn btn-ghost btn-sm" :aria-pressed="tierFilter === tier.tier" :data-testid="'share-tier-only-' + tier.tier" @click="toggleTierFilter(tier.tier)">{{ t('admin.shareScheduling.policy.tierCard.only') }}</button>
          </div>
        </div>
        <div
          class="flex min-w-44 items-center justify-center rounded-xl border border-dashed p-3 text-center text-xs text-gray-500 dark:text-dark-400"
          :class="dropTarget === newTier ? 'border-primary-400 ring-2 ring-primary-400' : 'border-gray-300 dark:border-dark-500'"
          data-testid="share-tier-card-new"
          @dragover.prevent="dropTarget = newTier"
          @dragleave="dropTarget = null"
          @drop.prevent="dropOnTier(newTier)"
        >
          {{ t('admin.shareScheduling.policy.tierCard.newTier', { tier: newTier }) }}
        </div>
        <p class="w-full text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.policy.tierCard.hint') }}</p>
      </div>

      <div v-if="picked.length" class="mt-3 flex flex-wrap items-end gap-3 rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60" data-testid="share-batch">
        <span class="w-full text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.selected', { count: picked.length }) }}</span>
        <label class="w-28 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.batch.tier') }}
          <input v-model="batch.tier" type="number" step="1" class="input mt-1" :placeholder="t('admin.shareScheduling.policy.batch.noChange')" data-testid="share-batch-tier">
        </label>
        <label class="w-32 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.batch.weight') }}
          <input v-model="batch.weight" type="number" :min="SHARE_MIN_WEIGHT" :max="SHARE_MAX_WEIGHT" step="1" class="input mt-1" :placeholder="t('admin.shareScheduling.policy.batch.noChange')" data-testid="share-batch-weight">
        </label>
        <label class="w-44 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.policy.batch.cacheDomain') }}
          <input v-model.trim="batch.domain" type="text" :maxlength="SHARE_MAX_CACHE_DOMAIN" class="input mt-1" :placeholder="t('admin.shareScheduling.policy.batch.noChange')" data-testid="share-batch-domain">
        </label>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || (batch.tier === '' && batch.weight === '' && batch.domain === '')" data-testid="share-batch-apply" @click="applyBatch">{{ t('admin.shareScheduling.policy.batch.apply') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="setDrain(true)">{{ t('admin.shareScheduling.policy.batch.drain') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="setDrain(false)">{{ t('admin.shareScheduling.policy.batch.undrain') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="resetPicked">{{ t('admin.shareScheduling.policy.batch.reset') }}</button>
        <button type="button" class="btn btn-ghost btn-sm" @click="picked = []">{{ t('admin.shareScheduling.policy.batch.clear') }}</button>
      </div>

      <div class="mt-4 overflow-x-auto">
        <table class="w-full min-w-[720px] text-left text-sm">
          <thead>
            <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
              <th class="w-8 p-2"><input type="checkbox" :checked="allVisiblePicked" :aria-label="t('admin.shareScheduling.policy.selectVisible')" @change="toggleVisible"></th>
              <th class="p-2">{{ t('admin.shareScheduling.policy.columns.account') }}</th>
              <th class="w-20 p-2">{{ t('admin.shareScheduling.policy.columns.tier') }}</th>
              <th class="w-28 p-2">{{ t('admin.shareScheduling.policy.columns.weight') }}</th>
              <th class="w-16 p-2">
                <span class="inline-flex items-center gap-1">{{ t('admin.shareScheduling.policy.columns.drain') }}<HelpTooltip :content="t('admin.shareScheduling.policy.drainHint')" /></span>
              </th>
              <th class="w-36 p-2">
                <span class="inline-flex items-center gap-1">{{ t('admin.shareScheduling.policy.columns.cacheDomain') }}<HelpTooltip :content="t('admin.shareScheduling.policy.cacheDomainHint')" /></span>
              </th>
              <th class="w-32 p-2">
                <span class="inline-flex items-center gap-1">{{ t('admin.shareScheduling.policy.columns.shares') }}<HelpTooltip :content="t('admin.shareScheduling.policy.sharesHint')" /></span>
              </th>
              <th class="w-16 p-2"><span class="sr-only">{{ t('admin.shareScheduling.policy.columns.actions') }}</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in pageRows" :key="row.account_id" class="border-b border-gray-100 align-middle last:border-0 dark:border-dark-700" :data-testid="'share-row-' + row.account_id">
              <td class="p-2"><input v-model="picked" type="checkbox" :value="row.account_id" :aria-label="t('admin.shareScheduling.policy.selectNamed', { name: rowName(row) })"></td>
              <td class="max-w-0 p-2">
                <div class="flex min-w-0 items-center gap-1.5">
                  <span
                    :draggable="!busy"
                    class="shrink-0 cursor-grab select-none text-gray-400 dark:text-dark-400"
                    :title="t('admin.shareScheduling.policy.tierCard.drag')"
                    aria-hidden="true"
                    :data-testid="'share-drag-' + row.account_id"
                    @dragstart="startDrag(row, $event)"
                    @dragend="endDrag"
                  >⠿</span>
                  <span class="truncate font-medium text-gray-900 dark:text-white" :title="rowName(row)">{{ rowName(row) }}</span>
                  <span v-if="!row.configured" class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[11px] text-gray-500 dark:bg-dark-700 dark:text-dark-400">{{ t('admin.shareScheduling.policy.usingDefault') }}</span>
                </div>
                <div class="mt-0.5 flex min-w-0 items-center gap-2 text-xs">
                  <span class="truncate text-gray-500 dark:text-dark-400">#{{ row.account_id }} · {{ row.platform }}/{{ row.type }} · P{{ row.priority }} · C{{ row.concurrency }}</span>
                  <span v-if="member(row.account_id)?.healthy" class="shrink-0 text-emerald-700 dark:text-emerald-300">{{ t('admin.shareScheduling.policy.healthy') }}</span>
                  <span v-else class="shrink-0 text-amber-700 dark:text-amber-300">
                    {{ labels.reason(member(row.account_id)?.reason) }}<template v-if="member(row.account_id)?.recover_at"> · {{ t('admin.shareScheduling.policy.recoverAt', { time: formatDateTime(member(row.account_id)?.recover_at) }) }}</template>
                  </span>
                </div>
              </td>
              <td class="p-2">
                <input v-model.number="row.tier" type="number" step="1" class="input w-full px-2" :aria-label="rowName(row) + ' ' + t('admin.shareScheduling.policy.columns.tier')" :data-testid="'share-tier-' + row.account_id" :disabled="busy" @input="row.configured = true">
              </td>
              <td class="p-2">
                <input v-model.number="row.weight" type="number" :min="SHARE_MIN_WEIGHT" :max="SHARE_MAX_WEIGHT" step="1" class="input w-full px-2" :aria-label="rowName(row) + ' ' + t('admin.shareScheduling.policy.columns.weight')" :data-testid="'share-weight-' + row.account_id" :disabled="busy" @input="row.configured = true">
              </td>
              <td class="p-2">
                <input v-model="row.drain" type="checkbox" :aria-label="t('admin.shareScheduling.policy.drainNamed', { name: rowName(row) })" :data-testid="'share-drain-' + row.account_id" :disabled="busy" @change="onDrainChange(row)">
                <label v-if="row.drain" class="mt-1 block w-44 text-[11px] text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.policy.drainUntil') }}
                  <input type="datetime-local" class="input mt-1 px-2 text-xs" :value="toLocalInput(row.drain_until)" :disabled="busy" @change="setDrainUntil(row, $event)">
                </label>
              </td>
              <td class="p-2">
                <input v-model.trim="row.cache_domain" type="text" :maxlength="SHARE_MAX_CACHE_DOMAIN" class="input w-full px-2" :placeholder="t('admin.shareScheduling.policy.cacheDomainPlaceholder')" :aria-label="rowName(row) + ' ' + t('admin.shareScheduling.policy.columns.cacheDomain')" :data-testid="'share-domain-' + row.account_id" :disabled="busy" @input="row.configured = true">
              </td>
              <td class="p-2 text-xs tabular-nums" :data-testid="'share-shares-' + row.account_id">
                <div class="font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.policy.shareEffective', { value: formatSharePercent(sharesOf(row).effective) }) }}</div>
                <div class="text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.policy.shareTarget', { value: formatSharePercent(sharesOf(row).target) }) }} · {{ t('admin.shareScheduling.policy.shareCapacity', { value: formatSharePercent(sharesOf(row).capacity) }) }}</div>
              </td>
              <td class="p-2 text-right">
                <button type="button" class="btn btn-ghost btn-sm" :disabled="busy || !row.configured" :aria-label="t('admin.shareScheduling.policy.resetNamed', { name: rowName(row) })" @click="resetShareRow(entry.draft, row)">{{ t('admin.shareScheduling.policy.reset') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="!filteredRows.length" class="space-y-3 py-6 text-center text-sm text-gray-600 dark:text-dark-300">
          <p>{{ search ? t('admin.shareScheduling.policy.emptySearch') : t('admin.shareScheduling.policy.emptyGroup') }}</p>
          <button v-if="search" type="button" class="btn btn-secondary btn-sm" @click="search = ''">{{ t('admin.shareScheduling.policy.clearSearch') }}</button>
          <RouterLink v-else to="/admin/accounts" class="btn btn-secondary btn-sm">{{ t('admin.shareScheduling.policy.manageAccounts') }}</RouterLink>
        </div>
      </div>
      <Pagination v-if="filteredRows.length > pageSize" class="mt-3" :total="filteredRows.length" :page="page" :page-size="pageSize" :show-page-size-selector="false" @update:page="page = $event" />
    </section>

    <details class="card p-5" data-testid="share-advanced">
      <summary class="cursor-pointer font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.policy.advanced') }}</summary>
      <fieldset class="mt-4 space-y-6" :disabled="busy">
        <div>
          <h3 class="text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.policy.sections.defaults') }}</h3>
          <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.defaultWeight') }}
              <select v-model="entry.draft.policy.defaults.weight" class="input mt-1" data-testid="share-default-weight" @change="refreshShareDefaults(entry.draft)">
                <option value="concurrency">{{ t('admin.shareScheduling.policy.defaultWeights.concurrency') }}</option>
                <option value="equal">{{ t('admin.shareScheduling.policy.defaultWeights.equal') }}</option>
              </select>
            </label>
          </div>
        </div>
        <div>
          <h3 class="text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.policy.sections.affinity') }}</h3>
          <label class="mt-3 flex items-start gap-2 text-sm text-gray-700 dark:text-dark-200">
            <input v-model="entry.draft.policy.affinity.enabled" type="checkbox" class="mt-1">
            <span>{{ t('admin.shareScheduling.policy.fields.affinityEnabled') }}<span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.policy.fields.affinityEnabledHint') }}</span></span>
          </label>
          <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.affinityKey') }}
              <select v-model="entry.draft.policy.affinity.key" class="input mt-1">
                <option v-for="value in ['auto', 'session', 'prefix', 'none']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.affinityKey.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.ttlSeconds') }}
              <input v-model.number="entry.draft.policy.affinity.ttl_seconds" type="number" min="300" max="86400" step="1" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.waitMode') }}
              <select v-model="entry.draft.policy.affinity.wait.mode" class="input mt-1">
                <option v-for="value in ['auto', 'fixed', 'off']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.waitMode.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.waitPer1k') }}
              <input v-model.number="entry.draft.policy.affinity.wait.ms_per_1k_tokens" type="number" min="0" max="5000" step="1" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.waitMax') }}
              <input v-model.number="entry.draft.policy.affinity.wait.max_ms" type="number" min="0" max="15000" step="100" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.maxWaiting') }}
              <input v-model.number="entry.draft.policy.affinity.wait.max_waiting" type="number" min="0" max="100" step="1" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.onSpill') }}
              <select v-model="entry.draft.policy.affinity.on_spill" class="input mt-1">
                <option v-for="value in ['rebind', 'keep']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.onSpill.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.softSpill') }}
              <input v-model.number="entry.draft.policy.affinity.soft_spill_max_seconds" type="number" min="0" max="1800" step="1" class="input mt-1">
              <span class="input-hint">{{ t('admin.shareScheduling.policy.fields.softSpillHint') }}</span>
            </label>
          </div>
        </div>
        <div>
          <h3 class="text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.policy.sections.cache') }}</h3>
          <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.cacheTTL') }}
              <input v-model.number="entry.draft.policy.cache.ttl_seconds" type="number" min="0" max="86400" step="1" class="input mt-1">
              <span class="input-hint">{{ t('admin.shareScheduling.policy.fields.cacheTTLHint') }}</span>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.realign') }}
              <select v-model="entry.draft.policy.cache.realign" class="input mt-1">
                <option v-for="value in ['cold', 'off']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.realign.' + value) }}</option>
              </select>
            </label>
          </div>
        </div>
        <div>
          <h3 class="text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.policy.sections.tiers') }}</h3>
          <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.onSaturated') }}
              <select v-model="entry.draft.policy.tiers.on_saturated" class="input mt-1">
                <option v-for="value in ['spill', 'wait']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.onSaturated.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.onFailed') }}
              <select v-model="entry.draft.policy.tiers.on_failed" class="input mt-1">
                <option v-for="value in ['next_tier', 'stop']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.onFailed.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.returnMode') }}
              <select v-model="entry.draft.policy.tiers.return" class="input mt-1">
                <option v-for="value in ['cold', 'immediate', 'session_end']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.returnMode.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.stableSeconds') }}
              <input v-model.number="entry.draft.policy.tiers.stable_seconds" type="number" min="0" max="3600" step="1" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.returnDeadline') }}
              <input v-model.number="entry.draft.policy.tiers.return_deadline_seconds" type="number" min="0" max="86400" step="1" class="input mt-1">
              <span class="input-hint">{{ t('admin.shareScheduling.policy.fields.returnDeadlineHint') }}</span>
            </label>
          </div>
        </div>
        <div>
          <h3 class="text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.shareScheduling.policy.sections.balance') }}</h3>
          <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.unit') }}
              <select v-model="entry.draft.policy.balance.unit" class="input mt-1" @change="onUnitChange">
                <option v-for="value in ['sessions', 'requests', 'tokens', 'standard_cost']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.unit.' + value) }}</option>
              </select>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.correction') }}
              <select v-model="entry.draft.policy.balance.correction" class="input mt-1" :disabled="entry.draft.policy.balance.unit === 'sessions'">
                <option v-for="value in ['off', 'gentle', 'strong']" :key="value" :value="value">{{ t('admin.shareScheduling.policy.options.correction.' + value) }}</option>
              </select>
              <span v-if="entry.draft.policy.balance.unit === 'sessions'" class="input-hint">{{ t('admin.shareScheduling.policy.fields.correctionHint') }}</span>
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.windowMinutes') }}
              <input v-model.number="entry.draft.policy.balance.window_minutes" type="number" min="5" max="1440" step="1" class="input mt-1">
            </label>
            <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.policy.fields.minVolume') }}
              <input v-model.number="entry.draft.policy.balance.min_volume" type="number" min="0" max="1000000" step="1" class="input mt-1">
            </label>
          </div>
        </div>
      </fieldset>
    </details>

    <section class="space-y-3">
      <div v-if="entry.conflict !== null" class="flex flex-wrap items-center gap-3 rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" role="alert" data-testid="share-conflict">
        <p class="min-w-0 flex-1">{{ entry.conflict > 0 ? t('admin.shareScheduling.policy.conflict', { version: entry.conflict }) : t('admin.shareScheduling.policy.conflictUnknown') }}</p>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="share-rebase" @click="store.rebase()">{{ t('admin.shareScheduling.policy.rebase') }}</button>
      </div>
      <div v-if="store.problems.length" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert" data-testid="share-problems">
        <p class="font-medium">{{ t('admin.shareScheduling.policy.problemsTitle') }}</p>
        <ul class="mt-1 list-inside list-disc">
          <li v-for="(problem, index) in store.problems" :key="index">{{ t('admin.shareScheduling.policy.problems.' + problem.key, problem.params ?? {}) }}</li>
        </ul>
      </div>
      <div v-if="store.serverProblems.length" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert" data-testid="share-server-problems">
        <p class="font-medium">{{ t('admin.shareScheduling.policy.serverProblems') }}</p>
        <ul class="mt-1 list-inside list-disc">
          <li v-for="(problem, index) in store.serverProblems" :key="index">{{ problem }}</li>
        </ul>
      </div>
      <p v-if="store.actionError" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert">{{ store.actionError }}</p>
      <p v-if="store.savedVersion !== null && !store.dirty" class="rounded-xl bg-emerald-50 p-3 text-sm text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300" role="status" data-testid="share-saved">{{ t('admin.shareScheduling.policy.saved', { version: store.savedVersion }) }}</p>
      <p v-if="store.rebasedVersion !== null" class="rounded-xl bg-sky-50 p-3 text-sm text-sky-800 dark:bg-sky-900/20 dark:text-sky-200" role="status">{{ t('admin.shareScheduling.policy.rebased', { version: store.rebasedVersion }) }}</p>
      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-200 pt-4 dark:border-dark-700">
        <p class="text-sm" :class="store.dirty ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500 dark:text-dark-400'" data-testid="share-draft-status">{{ store.dirty ? t('admin.shareScheduling.policy.dirty') : t('admin.shareScheduling.policy.clean') }}</p>
        <div class="flex flex-wrap gap-2">
          <button v-if="store.dirty" type="button" class="btn btn-secondary" :disabled="busy" data-testid="share-discard" @click="store.discard()">{{ t('admin.shareScheduling.policy.discard') }}</button>
          <button type="button" class="btn btn-primary" :disabled="busy || !store.dirty || entry.conflict !== null" data-testid="share-preview-save" @click="store.requestPreview()">
            {{ store.previewing ? t('admin.shareScheduling.policy.previewing') : t('admin.shareScheduling.policy.previewSave') }}
          </button>
        </div>
      </div>
    </section>

    <SharePreviewDialog :entry="entry" :preview="store.preview" :saving="store.saving" @confirm="store.save()" @close="store.closePreview()" />
    <ShareRebalanceDialog :show="rebalanceOpen" @close="rebalanceOpen = false" />
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Pagination from '@/components/common/Pagination.vue'
import SharePreviewDialog from './SharePreviewDialog.vue'
import ShareRebalanceDialog from './ShareRebalanceDialog.vue'
import type { ShareScheduler } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { formatDateTime } from '@/utils/format'
import {
  SHARE_MAX_CACHE_DOMAIN,
  SHARE_MAX_WEIGHT,
  SHARE_MIN_WEIGHT,
  draftShares,
  formatSharePercent,
  fromLocalInput,
  refreshShareDefaults,
  resetShareRow,
  toLocalInput,
  type ShareDraftRow,
  type ShareDraftShares,
  type ShareRowShares
} from '@/utils/shareScheduling'

const emit = defineEmits<{ (e: 'reload'): void }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const store = useShareSchedulingStore()
const schedulers: ShareScheduler[] = ['sub2api', 'shadow', 'share']
const pageSize = 50

const entry = computed(() => store.current)
const busy = computed(() => store.saving || store.previewing)
const search = ref('')
const page = ref(1)
const picked = ref<number[]>([])
const rebalanceOpen = ref(false)
const batch = reactive({ tier: '' as string | number, weight: '' as string | number, domain: '' })

const members = computed(() => new Map((entry.value?.view.members ?? []).map(m => [m.account_id, m])))
const member = (id: number) => members.value.get(id)
const shares = computed((): ShareDraftShares => entry.value
  ? draftShares(entry.value.draft, id => members.value.get(id)?.healthy === true)
  : { activeTier: null, byAccount: new Map() })
const sharesOf = (row: ShareDraftRow): ShareRowShares => shares.value.byAccount.get(row.account_id) ?? { target: 0, effective: 0, capacity: 0 }
const rowName = (row: ShareDraftRow) => row.name || t('admin.shareScheduling.policy.accountNumber', { id: row.account_id })

const filteredRows = computed(() => {
  const rows = entry.value?.draft.rows ?? []
  const q = search.value.toLowerCase()
  return (q ? rows.filter(row => `${row.name} ${row.account_id} ${row.cache_domain}`.toLowerCase().includes(q)) : rows)
    .filter(row => tierFilter.value === null || row.tier === tierFilter.value)
    .sort((a, b) => a.tier - b.tier || a.account_id - b.account_id)
})

// One card per tier: members, open weight and the share of new sessions
// the tier takes now. Cards are drop targets for rows.
const tiers = computed(() => {
  const byTier = new Map<number, { tier: number; count: number; weight: number; effective: number }>()
  for (const row of entry.value?.draft.rows ?? []) {
    const item = byTier.get(row.tier) ?? { tier: row.tier, count: 0, weight: 0, effective: 0 }
    item.count++
    if (!row.drain) item.weight += row.weight
    item.effective += sharesOf(row).effective
    byTier.set(row.tier, item)
  }
  return [...byTier.values()].sort((a, b) => a.tier - b.tier)
})
const newTier = computed(() => (tiers.value.length ? tiers.value[tiers.value.length - 1].tier + 1 : 1))
const tierFilter = ref<number | null>(null)
const dragged = ref<number[]>([])
const dropTarget = ref<number | null>(null)
const pageRows = computed(() => filteredRows.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const allVisiblePicked = computed(() => pageRows.value.length > 0 && pageRows.value.every(row => picked.value.includes(row.account_id)))

watch([search, tierFilter], () => { page.value = 1 })
watch(tiers, list => {
  if (tierFilter.value !== null && !list.some(item => item.tier === tierFilter.value)) tierFilter.value = null
})
watch(() => store.selected, () => {
  search.value = ''
  page.value = 1
  picked.value = []
  tierFilter.value = null
  rebalanceOpen.value = false
})

function pickTier(tier: number) {
  const ids = (entry.value?.draft.rows ?? []).filter(row => row.tier === tier).map(row => row.account_id)
  picked.value = [...new Set([...picked.value, ...ids])]
}

function toggleTierFilter(tier: number) {
  tierFilter.value = tierFilter.value === tier ? null : tier
}

// Dragging a selected row moves every selected row; any other row moves alone.
function startDrag(row: ShareDraftRow, event: DragEvent) {
  dragged.value = picked.value.includes(row.account_id) ? [...picked.value] : [row.account_id]
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', dragged.value.join(','))
  }
}

function endDrag() {
  dragged.value = []
  dropTarget.value = null
}

function dropOnTier(tier: number) {
  const ids = new Set(dragged.value)
  if (!busy.value) {
    for (const row of entry.value?.draft.rows ?? []) {
      if (!ids.has(row.account_id)) continue
      row.tier = tier
      row.configured = true
    }
  }
  endDrag()
}

function toggleVisible() {
  const ids = pageRows.value.map(row => row.account_id)
  picked.value = allVisiblePicked.value ? picked.value.filter(id => !ids.includes(id)) : [...new Set([...picked.value, ...ids])]
}

function pickedRows(): ShareDraftRow[] {
  const ids = new Set(picked.value)
  return (entry.value?.draft.rows ?? []).filter(row => ids.has(row.account_id))
}

function applyBatch() {
  const tier = batch.tier === '' ? null : Number(batch.tier)
  const weight = batch.weight === '' ? null : Number(batch.weight)
  for (const row of pickedRows()) {
    if (tier !== null) row.tier = tier
    if (weight !== null) row.weight = weight
    if (batch.domain !== '') row.cache_domain = batch.domain
    row.configured = true
  }
  batch.tier = ''
  batch.weight = ''
  batch.domain = ''
}

function setDrain(drain: boolean) {
  for (const row of pickedRows()) {
    row.drain = drain
    if (!drain) row.drain_until = ''
    row.configured = true
  }
}

function resetPicked() {
  if (!entry.value) return
  for (const row of pickedRows()) resetShareRow(entry.value.draft, row)
}

function onDrainChange(row: ShareDraftRow) {
  row.configured = true
  if (!row.drain) row.drain_until = ''
}

function setDrainUntil(row: ShareDraftRow, event: Event) {
  row.drain_until = fromLocalInput((event.target as HTMLInputElement).value)
  row.configured = true
}

function onUnitChange() {
  if (entry.value && entry.value.draft.policy.balance.unit === 'sessions') entry.value.draft.policy.balance.correction = 'off'
}
</script>
