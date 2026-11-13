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
        <span :class="statusClass" data-sq-drawer-status>{{ statusText(detail, now, t, te) }}</span>
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.priority') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.priority }}</b></span>
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.rate') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.rate.toFixed(3) }}</b></span>
        <span class="sq-text-3">{{ t('admin.schedQuality.drawer.concurrency') }}<b class="sq-num ml-1.5 font-medium text-gray-700 dark:text-gray-300">{{ detail.account.in_flight }} / {{ detail.account.concurrency }}</b></span>
      </div>
    </template>
    <template v-if="detail" #header-actions>
      <router-link
        v-if="canViewUsage"
        :to="{ path: '/admin/usage', query: { account_id: String(detail.account.id) } }"
        class="sq-link sq-focus px-1 text-sm"
        data-sq-trigger="drawer-records"
      >{{ t('admin.schedQuality.action.records') }}</router-link>
      <template v-if="canManage">
        <span v-if="failedGroups.has(0)" class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          data-sq-trigger="drawer-exempt"
          @click="onHeaderExempt"
        >{{ t(allGroupsExemption ? 'admin.schedQuality.action.undoExempt' : 'admin.schedQuality.action.exempt') }}</button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="recovering"
          :aria-busy="recovering"
          data-sq-trigger="drawer-reset"
          @click="startRecover"
        >{{ t('admin.schedQuality.action.reset') }}</button>
      </template>
    </template>

    <!-- Loading -->
    <div v-if="!detail && !poll.error.value" data-sq-drawer-loading>
      <span v-for="i in 4" :key="i" :class="['skeleton block h-3.5 w-full', i === 1 ? '' : 'mt-3.5']" />
    </div>

    <!-- Failed first load -->
    <div v-else-if="!detail" class="flex items-center gap-3 text-sm" data-sq-drawer-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="poll.refresh()">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>

    <template v-else>
      <SchedRecoverLine v-if="line" class="-mx-3 mb-6 rounded-lg" :line="line" @close="recoveryLines.delete(detail.account.id)" />

      <!-- Groups -->
      <section ref="groupsRef" data-sq-section="groups">
        <div class="mb-3 flex items-center gap-1.5">
          <h3 class="sq-section-title">{{ t('admin.schedQuality.drawer.byGroup') }}</h3>
          <SchedPauseMark :global-enabled="detail.global_enabled" :stale="detail.stale" />
        </div>
        <SchedDrawerGroups
          :groups="detail.groups"
          :now="now"
          :target-group-id="targetGroupId"
          :failed-groups="failedGroups"
          @order="(group) => openSchedBoard({ id: group.group_id, name: group.name }, drawer.model)"
          @override="(group, anchor) => openPopover(group.group_id, anchor)"
          @undo="(group) => clearExemption(group.exemption!.group_id, group.group_id)"
        />
      </section>

      <!-- Automatic stops -->
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
    </template>
  </BaseDrawer>

  <SchedOverridePopover
    :show="popover.open"
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
import SchedRecoverLine from './SchedRecoverLine.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedAutoState, SchedOverrideRequest } from '@/types/schedQuality'
import { secondsUntil, useSchedQualityClock, useSchedQualityPoll } from '@/composables/useSchedQuality'
import { closeSchedDrawer, markSchedChanged, openSchedBoard, openSchedDrawer, trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import { isRecovering, recoveryLines, useAccountRecovery } from '@/composables/useAccountRecovery'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { useAdminAccessStore } from '@/stores/adminAccess'
import { fmtCountdown, kindName, parentIdFromKey, statusText } from '@/utils/schedQualityFormat'

// Account detail drawer (FINAL §7): status, priority, rate and concurrency; per group its lines and per model its
// evidence, verdict, recovery time and level; the existing automatic stops; exemptions. One instance for the admin
// UI, opened through useSchedLayers; refreshes every 5 s while open, countdowns run on the local clock.
const props = defineProps<{
  /** Opens the IQ check records of an account; the link is hidden when not given. */
  onIqCheck?: (accountId: number) => void
  /** Opens the account edit dialog; the link is hidden when not given. */
  onEdit?: (accountId: number) => void
  /** Reads the account back on the page after a recovery from here. */
  onRecovered?: (accountId: number) => Promise<void> | void
}>()

const { t, te } = useI18n()
const { drawer, changed } = useSchedLayers()
// Write actions need accounts:manage; without it they are not shown.
const { canManage } = useAdminPermission('accounts')
const adminAccess = useAdminAccessStore()
const canViewUsage = computed(() => adminAccess.can('usage:view'))
const now = useSchedQualityClock()
const shellRef = ref<{ body: HTMLElement | null; holdFocus: () => () => void } | null>(null)
const groupsRef = ref<HTMLElement | null>(null)

const poll = useSchedQualityPoll(() => drawer.accountId, (signal) => schedQualityAPI.accountDetail(drawer.accountId!, { signal }))
const detail = computed(() => poll.data.value)
// Any quality action (here, in the list, in batches) reloads the open drawer; a recovery waits for this reload.
watch(changed, () => { if (drawer.accountId !== null) trackSchedReload(poll.refresh()) })

// Keep focus inside the drawer when a refresh re-renders the control that had it (spec 0.2, R6).
let restoreFocus: (() => void) | null = null
watch(() => poll.data.value, () => { restoreFocus = shellRef.value?.holdFocus() ?? null }, { flush: 'pre' })
watch(() => poll.data.value, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

const statusClass = computed(() => {
  const value = detail.value
  if (value?.status === 'disabled') return value.stop?.source === 'manual' ? 'sq-text-3' : 'sq-cool'
  return value?.status === 'observing' ? 'sq-text-3' : 'sq-text'
})

// Opened at a group (from a capsule): scroll its rows into view once the detail is in.
const targetGroupId = ref<number | null>(null)
let pendingScroll = true
watch(() => [drawer.accountId, drawer.raise] as const, ([id], previous) => {
  targetGroupId.value = drawer.groupId
  pendingScroll = true
  if (previous && id === previous[0]) void poll.refresh()
  if (detail.value && id === previous?.[0]) scrollToTarget()
})
watch(detail, (value) => { if (value && pendingScroll) scrollToTarget() })

function scrollToTarget() {
  pendingScroll = false
  void nextTick(() => {
    const body = shellRef.value?.body
    if (!body) return
    if (targetGroupId.value !== null) body.querySelector(`[data-sq-group-row="${targetGroupId.value}"]`)?.scrollIntoView({ block: 'center' })
    else if (drawer.toGroups && groupsRef.value) groupsRef.value.scrollIntoView({ block: 'start' })
    else body.scrollTop = 0
  })
}

const scopeGroups = computed(() => detail.value?.groups.map((g) => ({ id: g.group_id, name: g.name })) ?? [])

// 恢复 (FINAL §5): any time; its result line shows here and under the account's row.
const { recover } = useAccountRecovery()
const recovering = computed(() => !!detail.value && isRecovering(detail.value.account.id))
const line = computed(() => (detail.value ? recoveryLines.get(detail.value.account.id) : undefined))
function startRecover() {
  const account = detail.value?.account
  if (account) void recover(account.id, () => props.onRecovered?.(account.id))
}

// Exemptions: the header button covers every group; group rows set or cancel their own.
const allGroupsExemption = computed(() => detail.value?.exemptions.find((e) => e.group_id === 0) ?? null)
function onHeaderExempt(event: MouseEvent) {
  if (allGroupsExemption.value) void clearExemption(0, 0)
  else openPopover(0, event.currentTarget as HTMLElement)
}

const popover = reactive({ open: false, groupId: 0, anchor: null as HTMLElement | null, busy: false, failed: false })
function openPopover(groupId: number, anchor: HTMLElement) {
  Object.assign(popover, { open: true, groupId, anchor, busy: false, failed: false })
}

async function applyOverride(request: SchedOverrideRequest) {
  const id = drawer.accountId
  if (id === null || !canManage.value) return
  popover.busy = true
  popover.failed = false
  try {
    await schedQualityAPI.setOverride(id, request)
    popover.open = false
    void markSchedChanged()
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

/** `groupId` is the exemption to cancel (0 = every group); `markAt` the row (or 0, the header) whose button was clicked. */
async function clearExemption(groupId: number, markAt: number) {
  const id = drawer.accountId
  if (id === null) return
  try {
    await schedQualityAPI.clearOverride(id, groupId)
    void markSchedChanged()
  } catch {
    flash(failedGroups, markAt)
  }
}

// Automatic stops.
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
    if (result.cleared) void markSchedChanged()
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
