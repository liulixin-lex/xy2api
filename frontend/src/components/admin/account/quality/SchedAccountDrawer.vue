<template>
  <BaseDrawer
    ref="shellRef"
    :show="drawer.accountId !== null"
    :raise-key="drawer.raise"
    :close-label="t('admin.schedQuality.aria.close')"
    @close="closeSchedDrawer"
  >
    <template #header="{ titleId }">
      <h2 :id="titleId" class="truncate text-lg font-semibold text-gray-900 dark:text-white">{{ detail?.account.name || drawer.accountName }}</h2>
      <div v-if="detail" class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.priority') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.priority }}</b></span>
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.rate') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.rate.toFixed(3) }}</b></span>
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.concurrency') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.in_flight }} / {{ detail.account.concurrency }}</b></span>
      </div>
    </template>
    <template v-if="detail && canManage" #header-actions>
      <span v-if="testFailed" class="sq-fail-word text-sm" data-sq-test-failed>{{ t('admin.schedQuality.result.failed') }}</span>
      <button
        type="button"
        class="btn btn-secondary btn-sm aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
        :aria-disabled="testRunning || testBusy"
        data-sq-trigger="drawer-test"
        @click="startTest"
      >{{ t('admin.schedQuality.action.test') }}</button>
      <template v-for="mode in OVERRIDE_MODES" :key="mode">
        <span v-if="failedGroups.has(0) && allGroupsOverride?.mode === mode" class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :data-sq-trigger="`drawer-${mode}`"
          @click="onHeaderOverride(mode, $event)"
        >{{ headerOverrideLabel(mode) }}</button>
      </template>
      <button
        v-if="recoverable"
        type="button"
        class="btn btn-primary btn-sm"
        :disabled="recovering"
        :aria-busy="recovering"
        data-sq-trigger="drawer-reset"
        @click="startRecover"
      >{{ t('admin.schedQuality.action.reset') }}</button>
    </template>

    <!-- Loading -->
    <div v-if="!detail && !poll.error.value" data-sq-drawer-loading>
      <div class="grid grid-cols-3 gap-3">
        <div v-for="i in 3" :key="i" class="sq-tile"><span class="skeleton block h-3 w-8" /><span class="skeleton mt-2 block h-6 w-[72px]" /></div>
      </div>
      <span v-for="i in 3" :key="`line-${i}`" :class="['skeleton block h-3.5 w-full', i === 1 ? 'mt-8' : 'mt-3.5']" />
    </div>

    <!-- Failed first load -->
    <div v-else-if="!detail" class="flex items-center gap-3 text-sm" data-sq-drawer-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="poll.refresh()">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>

    <template v-else>
      <!-- Model tabs, cards, per-class first output -->
      <section>
        <div v-if="detail.models.length" v-roving-tabs role="tablist" :aria-label="t('admin.schedQuality.aria.model')" class="mb-3 flex flex-wrap gap-1">
          <button
            v-for="m in detail.models"
            :key="m.model"
            type="button"
            role="tab"
            :aria-selected="m.model === tab"
            :tabindex="m.model === tab ? 0 : -1"
            class="sq-tab sq-focus"
            @click="selectTab(m.model)"
          >{{ m.model }}</button>
        </div>
        <div class="grid grid-cols-3 gap-3">
          <div v-for="tile in tiles" :key="tile.dim" class="sq-tile min-w-0">
            <div class="text-xs sq-text-3">{{ t(`admin.schedQuality.dim.${tile.dim}`) }}</div>
            <span
              v-tip="tile.tip"
              tabindex="0"
              :class="['sq-focus sq-num mt-1 inline-block text-2xl font-semibold leading-8', tile.text === '—' ? 'sq-text-3' : 'sq-text']"
            >{{ tile.text }}</span>
          </div>
        </div>
        <div class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-[13px] md:grid-cols-4">
          <div v-for="cls in CLASSES" :key="cls" class="flex gap-2">
            <span class="sq-text-3">{{ t(`admin.schedQuality.class.${cls}`) }}</span>
            <b class="sq-num font-medium text-gray-700 dark:text-gray-300">{{ classValue(cls) }}</b>
          </div>
        </div>
      </section>

      <!-- Groups -->
      <section ref="groupsRef" class="mt-8" data-sq-section="groups">
        <div class="mb-3 flex items-center gap-1.5">
          <h3 class="sq-section-title">{{ t('admin.schedQuality.drawer.byGroup') }}</h3>
          <SchedPauseMark :global-enabled="detail.global_enabled" :stale="detail.stale" />
        </div>
        <SchedDrawerGroups
          :groups="detail.groups"
          :model="tab"
          :card="card"
          :now="now"
          :target-group-id="targetGroupId"
          :failed-groups="failedGroups"
          @board="(group) => openSchedBoard({ id: group.group_id, name: group.name }, card?.request_model || null)"
          @override="(group, mode, anchor) => openPopover(mode, group.group_id, anchor)"
          @undo="undoOverride"
        />
      </section>

      <!-- Auto states -->
      <section class="mt-8">
        <h3 class="sq-section-title mb-3">{{ t('admin.schedQuality.drawer.autoStates') }}</h3>
        <div v-if="detail.auto_states.length" class="-mx-3 overflow-x-auto">
          <table class="sq-table" :aria-label="t('admin.schedQuality.drawer.autoStates')">
            <tbody>
              <tr v-for="item in detail.auto_states" :key="item.key">
                <td>{{ kindName(item.kind, t, te) }}</td>
                <td class="sq-text-3">{{ scopeLabel(item) }}</td>
                <td class="sq-r sq-num w-[84px]">{{ autoValue(item) }}</td>
                <td class="sq-r w-[96px]">
                  <span v-if="failedKeys.has(item.key)" class="sq-fail-word mr-2">{{ t('admin.schedQuality.result.failed') }}</span>
                  <button
                    v-if="item.clearable && canManage"
                    type="button"
                    class="btn btn-secondary sq-btn-xs sq-focus"
                    :data-sq-trigger="`auto:${item.key}`"
                    @click="clearAuto(item)"
                  >{{ t('admin.schedQuality.action.clear') }}</button>
                  <button
                    v-else-if="gotoOf(item)"
                    type="button"
                    class="sq-link sq-focus text-sm"
                    :data-sq-trigger="`goto:${item.key}`"
                    @click="gotoOf(item)!.run()"
                  >{{ gotoOf(item)!.label }}</button>
                  <span v-else class="sq-text-3">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="sq-text-3 text-sm">—</p>
      </section>

      <!-- Test -->
      <section class="mt-8">
        <div class="mb-3 flex items-center justify-between gap-3">
          <h3 class="sq-section-title">{{ t('admin.schedQuality.drawer.test') }}</h3>
          <div v-if="test" class="flex items-center gap-2 text-[13px]" data-sq-test-head>
            <span class="text-xs font-medium text-gray-700 dark:text-gray-300">{{ t(`admin.schedQuality.test.${test.kind === 'screen' ? 'screen' : 'full'}`) }}</span>
            <span v-if="test.running" class="sq-test">{{ t('admin.schedQuality.state.testing') }}</span>
            <template v-else>
              <span class="sq-text-3 sq-num">{{ fmtTestTime(test.at) }}</span>
              <span :class="testEnd === 'done' ? 'sq-text' : 'text-gray-500 dark:text-dark-400'">{{ t(`admin.schedQuality.test.${testEnd}`) }}</span>
            </template>
          </div>
        </div>
        <div v-if="test" class="-mx-3 overflow-x-auto">
          <table class="sq-table">
            <thead>
              <tr>
                <th scope="col">{{ t('admin.schedQuality.col.class') }}</th>
                <th class="w-[112px]" aria-hidden="true" />
                <th scope="col" class="sq-r w-[84px]">{{ t('admin.schedQuality.dim.ttfo') }}</th>
                <th class="w-[264px]" aria-hidden="true" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="tier in testTiers" :key="tier">
                <td>{{ t(`admin.schedQuality.tier.${tier}`) }}</td>
                <td />
                <td class="sq-r sq-num">{{ tierTtfo(tier) }}</td>
                <td />
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="sq-text-3 text-sm">{{ t('admin.schedQuality.state.nodata') }}</p>
      </section>

      <!-- Recent requests -->
      <section class="mt-8">
        <h3 class="sq-section-title mb-3">{{ t('admin.schedQuality.drawer.recent') }}</h3>
        <template v-if="recent.length">
          <div class="-mx-3 overflow-x-auto">
            <table class="sq-table">
              <thead>
                <tr>
                  <th scope="col">{{ t('admin.schedQuality.col.time') }}</th>
                  <th scope="col">{{ t('admin.schedQuality.col.group') }}</th>
                  <th scope="col">{{ t('admin.schedQuality.col.class') }}</th>
                  <th scope="col" class="sq-r w-[84px]">{{ t('admin.schedQuality.dim.ttfo') }}</th>
                  <th scope="col" class="sq-r w-[84px]">{{ t('admin.schedQuality.col.result') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(attempt, index) in shownRecent" :key="`${attempt.at}-${index}`" :class="{ 'sq-weak': attemptWeak(attempt) }">
                  <td class="sq-num sq-text-3">{{ fmtClock(attempt.at) }}</td>
                  <td>{{ groupNames.get(attempt.group_id) ?? `#${attempt.group_id}` }}</td>
                  <td>{{ enumWord(attempt.class, SCHED_CLASSES, 'admin.schedQuality.class', t) }}</td>
                  <td class="sq-r sq-num">{{ fmtTtfo(attempt.ttfo_ms, false, t) }}</td>
                  <td class="sq-r">{{ resultWord(attempt) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <button
            v-if="recent.length > RECENT_SHORT"
            type="button"
            class="btn btn-ghost btn-sm mt-2"
            :aria-expanded="recentAll"
            data-sq-trigger="recent-toggle"
            @click="recentAll = !recentAll"
          >{{ t(recentAll ? 'admin.schedQuality.action.collapse' : 'admin.schedQuality.action.expand') }}</button>
        </template>
        <p v-else class="sq-text-3 text-sm">{{ t('admin.schedQuality.state.nodata') }}</p>
      </section>
    </template>
  </BaseDrawer>

  <SchedOverridePopover
    :show="popover.open"
    :mode="popover.mode"
    :anchor="popover.anchor"
    :groups="scopeGroups"
    :default-group-id="popover.groupId"
    :busy="popover.busy"
    :failed="popover.failed"
    @close="popover.open = false"
    @confirm="applyOverride"
  />

  <BaseDialog :show="proxyItem !== null" :title="t('admin.schedQuality.proxy.title')" width="narrow" @close="proxyItem = null">
    <p class="text-sm text-gray-900 dark:text-gray-100">
      <span class="sq-text-3">{{ t('admin.schedQuality.proxy.affected') }}</span>
      <span class="sq-num ml-2 font-medium">{{ proxyItem?.shared ?? 0 }}</span>
    </p>
    <template #footer>
      <button type="button" class="btn btn-secondary btn-sm" @click="proxyItem = null">{{ t('admin.schedQuality.action.cancel') }}</button>
      <button type="button" class="btn btn-primary btn-sm" @click="confirmProxyClear">{{ t('admin.schedQuality.action.clear') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDrawer from '@/components/common/BaseDrawer.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import SchedPauseMark from './SchedPauseMark.vue'
import SchedDrawerGroups from './SchedDrawerGroups.vue'
import SchedOverridePopover from './SchedOverridePopover.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type {
  SchedAccountGroup, SchedAutoState, SchedOverrideMode, SchedOverrideRequest, SchedRecentAttempt, SchedRequestClass, SchedTestTier
} from '@/types/schedQuality'
import { secondsUntil, useSchedQualityClock, useSchedQualityPoll } from '@/composables/useSchedQuality'
import { closeSchedDrawer, markSchedChanged, openSchedBoard, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import { isRecovering, useAccountRecovery } from '@/composables/useAccountRecovery'
import { useAdminPermission } from '@/composables/useAdminPermission'
import {
  attemptResult, attemptWeak, enumWord, fmtCacheRatio, fmtClock, fmtCountdown, fmtErrorRate, fmtTestTime, fmtTtfo, kindName,
  parentIdFromKey, SCHED_CLASSES, testResult
} from '@/utils/schedQualityFormat'
import { vRovingTabs } from '@/utils/rovingTabs'
import { vTip } from '@/utils/tip'

// Account quality drawer (design spec B). One instance for the admin UI; opened through useSchedLayers. Refreshes
// every 5 s while open; countdowns run on the local clock.
const props = defineProps<{
  /** Opens the IQ check records of an account; the link is hidden when not given. */
  onIqCheck?: (accountId: number) => void
  /** Opens the account edit dialog; the link is hidden when not given. */
  onEdit?: (accountId: number) => void
  /** Reads the account back on the page after a recovery from here. */
  onRecovered?: (accountId: number) => Promise<void> | void
}>()

const OVERRIDE_MODES: SchedOverrideMode[] = ['demote', 'exempt']
const CLASSES: SchedRequestClass[] = ['light', 'regular', 'heavy', 'xheavy']
const TIERS: SchedTestTier[] = ['small', 'medium', 'large']
const SCREEN_TIERS: SchedTestTier[] = ['small']
const RECENT_SHORT = 10
const RECENT_LONG = 50

const { t, te } = useI18n()
const { drawer, changed } = useSchedLayers()
// Write actions need accounts:manage; without it they are not shown.
const { canManage } = useAdminPermission('accounts')
const now = useSchedQualityClock()
const shellRef = ref<{ body: HTMLElement | null; holdFocus: () => () => void } | null>(null)
const groupsRef = ref<HTMLElement | null>(null)

const poll = useSchedQualityPoll(() => drawer.accountId, (signal) => schedQualityAPI.accountDetail(drawer.accountId!, { signal }))
const detail = computed(() => poll.data.value)
// Any quality action (here, in the reset dialog, in batches) refreshes the open drawer.
watch(changed, () => { if (drawer.accountId !== null) void poll.refresh() })

// Keep focus inside the drawer when a refresh re-renders the control that had it (spec 0.2, R6).
let restoreFocus: (() => void) | null = null
watch(() => poll.data.value, () => { restoreFocus = shellRef.value?.holdFocus() ?? null }, { flush: 'pre' })
watch(() => poll.data.value, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

// A failed test (K28, same rule as the batch) shows "失败" next to 测试 through the refresh the test triggers and
// until the next one; another account clears it.
const testFailedRefreshes = ref(0)

// Model tab: the requested model when the detail has it, else the first one.
const tab = ref<string | null>(null)
const targetGroupId = ref<number | null>(null)
const recentAll = ref(false)
let pendingScroll = true

watch(() => [drawer.accountId, drawer.raise] as const, ([id], previous) => {
  targetGroupId.value = drawer.groupId
  if (id !== previous?.[0]) testFailedRefreshes.value = 0
  recentAll.value = false
  pendingScroll = true
  if (previous && id === previous[0]) void poll.refresh()
  if (detail.value && id === previous?.[0]) applyRequest()
})

watch(detail, (value) => {
  if (!value) return
  const models = value.models.map((m) => m.model)
  if (pendingScroll) applyRequest()
  else if (!tab.value || !models.includes(tab.value)) tab.value = models[0] ?? null
})

function applyRequest() {
  const models = detail.value?.models.map((m) => m.model) ?? []
  tab.value = drawer.model && models.includes(drawer.model) ? drawer.model : (models[0] ?? null)
  pendingScroll = false
  void nextTick(() => {
    const body = shellRef.value?.body
    if (!body) return
    if (targetGroupId.value !== null) {
      body.querySelector(`[data-sq-group-row="${targetGroupId.value}"]`)?.scrollIntoView({ block: 'center' })
    } else if (drawer.toGroups && groupsRef.value) {
      groupsRef.value.scrollIntoView({ block: 'start' })
    } else {
      body.scrollTop = 0
    }
  })
}

function selectTab(model: string) {
  tab.value = model
  targetGroupId.value = null
}

const card = computed(() => detail.value?.models.find((m) => m.model === tab.value))
const tiles = computed(() => {
  const c = card.value
  return [
    { dim: 'ttfo', text: c ? fmtTtfo(c.ttfo.p90_ms, c.ttfo.p90_timeout, t) : '—', tip: c ? t('admin.schedQuality.tip.samples', { n: c.ttfo.n }) : '' },
    { dim: 'error', text: fmtErrorRate(c?.error.rate), tip: c ? t('admin.schedQuality.tip.samples', { n: c.error.n }) : '' },
    { dim: 'cache', text: fmtCacheRatio(c?.cache.ratio), tip: c ? t('admin.schedQuality.tip.samples', { n: c.cache.turns }) : '' }
  ]
})
const classValue = (cls: SchedRequestClass) => fmtTtfo(card.value?.ttfo.by_class.find((b) => b.class === cls)?.p90_ms, false, t)

const groupNames = computed(() => new Map(detail.value?.groups.map((g) => [g.group_id, g.name]) ?? []))
const scopeGroups = computed(() => detail.value?.groups.map((g) => ({ id: g.group_id, name: g.name })) ?? [])

// Test of the selected model (contract delta E): a screen shows only the small tier; the end word follows `clean`,
// and a test without any round is void.
const test = computed(() => card.value?.test ?? null)
const testRunning = computed(() => !!test.value?.running)
const testTiers = computed(() => (test.value?.kind === 'screen' ? SCREEN_TIERS : TIERS))
const testEnd = computed(() => {
  const value = test.value
  if (!value?.tiers.length) return 'aborted'
  return value.clean ? 'done' : 'unfinished'
})
function tierTtfo(tier: SchedTestTier) {
  const row = test.value?.tiers.find((x) => x.tier === tier)
  if (!row) return '—'
  if (row.timeouts > 0) return t('admin.schedQuality.result.timeout')
  const values = row.ttfo_ms.filter((v): v is number => v !== null)
  return values.length ? fmtTtfo(Math.max(...values), false, t) : '—'
}

// Recent requests of the selected model.
const recent = computed(() => detail.value?.recent_attempts.filter((a) => a.model === tab.value) ?? [])
const shownRecent = computed(() => recent.value.slice(0, recentAll.value ? RECENT_LONG : RECENT_SHORT))
const resultWord = (attempt: SchedRecentAttempt) => {
  const result = attemptResult(attempt)
  return t(result ? `admin.schedQuality.result.${result}` : 'admin.schedQuality.unknown')
}

// Header actions.
// 恢复 shows when there is something it clears: a cooling group row or a clearable auto state other than the shared
// proxy circuit (contract delta F3).
const { recover } = useAccountRecovery()
const recoverable = computed(() => {
  const value = detail.value
  if (!value) return false
  return value.groups.some((group) => group.by_model.some((row) => row.state === 'cooling')) ||
    value.auto_states.some((item) => item.clearable && item.kind !== 'proxy_circuit')
})
const recovering = computed(() => !!detail.value && isRecovering(detail.value.account.id))
function startRecover() {
  const account = detail.value?.account
  if (account) void recover({ id: account.id, name: account.name }, () => props.onRecovered?.(account.id))
}

const testBusy = ref(false)
const testFailed = computed(() => testFailedRefreshes.value > 0)
watch(detail, () => { if (testFailedRefreshes.value > 0) testFailedRefreshes.value-- })
async function startTest() {
  const id = drawer.accountId
  if (id === null || testRunning.value || testBusy.value) return
  testBusy.value = true
  testFailedRefreshes.value = 0
  let result: ReturnType<typeof testResult> = 'failed'
  try {
    result = testResult((await schedQualityAPI.startTest(id, tab.value ?? undefined)).tests)
  } catch {
    // A request error is a failed test.
  } finally {
    testBusy.value = false
  }
  if (result === 'failed' && drawer.accountId === id) testFailedRefreshes.value = 2
  markSchedChanged()
}

const allGroupsOverride = computed(() => detail.value?.all_groups_override ?? null)
const headerOverrideLabel = (mode: SchedOverrideMode) =>
  allGroupsOverride.value?.mode === mode
    ? t(mode === 'demote' ? 'admin.schedQuality.action.undoDemote' : 'admin.schedQuality.action.undoExempt')
    : t(`admin.schedQuality.action.${mode}`)

function onHeaderOverride(mode: SchedOverrideMode, event: MouseEvent) {
  if (allGroupsOverride.value?.mode === mode) void clearOverride(0, 0)
  else openPopover(mode, 0, event.currentTarget as HTMLElement)
}

const popover = reactive({ open: false, mode: 'demote' as SchedOverrideMode, groupId: 0, anchor: null as HTMLElement | null, busy: false, failed: false })
function openPopover(mode: SchedOverrideMode, groupId: number, anchor: HTMLElement) {
  Object.assign(popover, { open: true, mode, groupId, anchor, busy: false, failed: false })
}

async function applyOverride(request: SchedOverrideRequest) {
  const id = drawer.accountId
  if (id === null || !canManage.value) return
  popover.busy = true
  popover.failed = false
  try {
    await schedQualityAPI.setOverride(id, request)
    popover.open = false
    markSchedChanged()
  } catch {
    popover.failed = true
  } finally {
    popover.busy = false
  }
}

// Failures of one-click actions show "失败" next to the control for 2 s; group 0 is the header button.
const failedGroups = reactive(new Set<number>())
const failedKeys = reactive(new Set<string>())
function flash<T>(set: Set<T>, key: T) {
  set.add(key)
  setTimeout(() => set.delete(key), 2000)
}

/** `markAt` is the row (or 0, the header) whose control was clicked. */
async function clearOverride(groupId: number, markAt: number) {
  const id = drawer.accountId
  if (id === null) return
  try {
    await schedQualityAPI.clearOverride(id, groupId)
    markSchedChanged()
  } catch {
    flash(failedGroups, markAt)
  }
}

function undoOverride(group: SchedAccountGroup) {
  void clearOverride(group.override?.all_groups ? 0 : group.group_id, group.group_id)
}

// Auto states.
function scopeLabel(item: SchedAutoState) {
  if (item.scope === 'account') return t('admin.schedQuality.scope.account')
  if (item.scope === 'proxy') return t('admin.schedQuality.scope.proxy')
  return item.model || '—'
}

function autoValue(item: SchedAutoState) {
  if (item.until) return fmtCountdown(secondsUntil(item.until, now.value) ?? 0)
  if (item.count) return `${item.count[0]} / ${item.count[1]}`
  return '—'
}

function gotoOf(item: SchedAutoState): { label: string; run: () => void } | null {
  const id = drawer.accountId
  if (id === null) return null
  if (item.kind === 'iq_gate' && props.onIqCheck) return { label: t('admin.schedQuality.goto.iq'), run: () => props.onIqCheck!(id) }
  if (item.kind === 'parent_blocked') {
    const parent = parentIdFromKey(item.key)
    return parent === null ? null : { label: t('admin.schedQuality.goto.parent'), run: () => openSchedDrawer({ id: parent }) }
  }
  if (['manual_off', 'expired', 'quota'].includes(item.kind) && props.onEdit) {
    return { label: t('admin.schedQuality.goto.edit'), run: () => props.onEdit!(id) }
  }
  return null
}

const proxyItem = shallowRef<SchedAutoState | null>(null)

async function runClear(item: SchedAutoState) {
  const id = drawer.accountId
  if (id === null) return
  try {
    const result = await schedQualityAPI.clearAutoState(id, item.key)
    if (result.cleared) markSchedChanged()
    else flash(failedKeys, item.key)
  } catch {
    flash(failedKeys, item.key)
  }
}

function clearAuto(item: SchedAutoState) {
  if (item.kind === 'proxy_circuit') proxyItem.value = item
  else void runClear(item)
}

function confirmProxyClear() {
  const item = proxyItem.value
  proxyItem.value = null
  if (item && canManage.value) void runClear(item)
}

// A write layer opened before accounts:manage was withdrawn closes with it.
watch(canManage, (allowed) => {
  if (allowed) return
  popover.open = false
  proxyItem.value = null
})

defineExpose({ refresh: () => poll.refresh() })
</script>
