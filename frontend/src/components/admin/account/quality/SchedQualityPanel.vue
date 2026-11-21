<template>
  <Teleport to="body">
    <Transition :css="false" @enter="onEnter" @leave="onLeave">
      <aside
        v-if="open"
        ref="rootRef"
        class="qpanel"
        :style="{ top: `${top}px`, zIndex: layer.zIndex.value }"
        role="dialog"
        :aria-modal="wide ? 'false' : 'true'"
        :aria-labelledby="titleId"
        tabindex="-1"
        data-quality-panel
        @keydown.tab="!wide && layer.trapTab($event)"
      >
        <!-- One line: the account, what can be done to it, close. -->
        <header class="qp-head">
          <h2 :id="titleId" ref="titleRef" class="qp-name" tabindex="-1">{{ (fresh && detail?.account.name) || drawer.accountName }}</h2>
          <div v-if="fresh && detail && canManage" class="qp-actions">
            <span v-if="failedGroups.has(0)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="!allExemption && !!detail.stop"
              :data-qp-trigger="allExemption ? 'unexempt:0' : 'exempt:0'"
              @click="onHeaderExempt($event)"
            >{{ allExemption ? t('admin.schedQuality.action.undoExempt') : t('admin.schedQuality.action.exempt') }}</button>
            <!-- A disabled button takes no focus and, in some browsers, no mouse events: the reason sits on this wrapper
                 (reachable by Tab then), and the disabled button lets the pointer through to it. -->
            <span
              v-tip="recoverable ? '' : t('admin.schedQuality.panel.recoverNone')"
              class="inline-flex"
              :tabindex="recoverable ? undefined : 0"
              data-qp-recover
            >
              <button
                type="button"
                class="btn btn-secondary btn-sm disabled:pointer-events-none"
                :disabled="!recoverable || recovering"
                :aria-busy="recovering"
                data-qp-trigger="reset"
                @click="startRecover"
              >{{ t('admin.schedQuality.action.reset') }}</button>
            </span>
          </div>
          <button type="button" class="qp-close" :aria-label="t('admin.schedQuality.aria.close')" data-qp-close @click="close(true)">
            <Icon name="x" size="md" />
          </button>
        </header>

        <!-- Switched to another account: the previous one's content stays, dimmed and out of reach, until the new one lands
             (a failure shows its retry instead, within reach: 7h-1). -->
        <div
          ref="bodyRef"
          class="qp-body"
          :class="{ 'opacity-60': switching }"
          :inert="switching || undefined"
          :aria-busy="switching ? 'true' : undefined"
          data-qp-body
        >
          <div v-if="failed" class="qp-failed" data-qp-failed>
            <span class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
            <span v-if="panel.failedReason" class="qp-failed__why" data-qp-failed-reason>{{ panel.failedReason }}</span>
            <button type="button" class="btn btn-secondary btn-sm" @click="retry(SCHED_PANEL_TASK)">{{ t('admin.schedQuality.action.retry') }}</button>
          </div>
          <div v-else-if="!detail" class="qp-loading" data-qp-loading>
            <span v-for="i in 5" :key="i" class="skeleton block h-3.5 w-full" />
          </div>

          <template v-else>
            <!-- The account's automatic states, when there are any: what stops it besides quality, first. -->
            <section v-if="detail.auto_states.length" class="qp-section" data-qp-section="auto" :aria-label="t('admin.schedQuality.drawer.autoStates')">
              <div class="alist">
                <div v-for="item in detail.auto_states" :key="item.key" class="arow" :data-qp-auto="item.key">
                  <span class="arow__k">{{ kindName(item.kind, t, te) }}</span>
                  <span class="arow__m">{{ scopeLabel(item) }}</span>
                  <span class="arow__t">{{ autoValue(item) }}</span>
                  <span class="arow__a">
                    <span v-if="failedKeys.has(item.key)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
                    <button
                      v-if="item.clearable && canManage"
                      type="button"
                      class="qp-text-btn"
                      :data-qp-trigger="`auto:${item.key}`"
                      @click="clearAuto(item)"
                    >{{ t('admin.schedQuality.action.clear') }}</button>
                    <button
                      v-else-if="gotoOf(item)"
                      type="button"
                      class="qp-text-btn"
                      :data-qp-trigger="`goto:${item.key}`"
                      @click="gotoOf(item)!.run()"
                    >{{ gotoOf(item)!.label }}</button>
                  </span>
                </div>
              </div>
            </section>

            <!-- Model tabs: whole names, wrapping onto another line when they do not fit; a dot before a model with a group
                 at the rear (amber) or on trial (blue). -->
            <div v-if="models.length" v-roving-tabs role="tablist" :aria-label="t('admin.schedQuality.aria.model')" class="qp-seg">
              <button
                v-for="model in models"
                :key="model"
                type="button"
                role="tab"
                class="qp-seg__btn"
                :aria-selected="model === tab"
                :tabindex="model === tab ? 0 : -1"
                :data-qp-tab="model"
                @click="tab = model"
              >
                <span v-if="tabMarks.has(model)" class="qp-seg__dot" :class="`is-${tabMarks.get(model)}`" aria-hidden="true" :data-qp-tab-mark="tabMarks.get(model)" />
                <span class="qp-seg__label">{{ model }}</span>
                <span v-if="tabMarks.has(model)" class="sr-only">{{ t(`admin.schedQuality.panel.tabMark.${tabMarks.get(model)}`) }}</span>
              </button>
            </div>

            <SchedPanelGroups
              :detail="detail"
              :model="tab"
              :target-group-id="targetGroupId"
              :group-info="groupInfo"
              :can-manage="canManage"
              :failed-groups="failedGroups"
              :open-group-id="popover.open ? popover.groupId : null"
              @place="openPlace"
            />


            <SchedDecisions :account-id="detail.account.id" :group="pillOf" :model="tab" />
          </template>
        </div>
      </aside>
    </Transition>
  </Teleport>

  <SchedOverridePopover
    :show="popover.open"
    :anchor="popover.anchor"
    :groups="scopeGroups"
    :default-group-id="popover.groupId"
    :busy="popover.busy"
    :failed="popover.failed"
    :undo="popover.undo"
    :align="popover.groupId === 0 ? 'end' : 'start'"
    @close="popover.open = false"
    @confirm="applyExemption"
    @undo="undoExemption"
  />

  <BaseDialog :show="proxyItem !== null" :title="t('admin.schedQuality.proxy.title')" width="narrow" @close="proxyItem = null">
    <p class="text-sm text-fg-primary">
      <span class="text-fg-secondary">{{ t('admin.schedQuality.proxy.affected') }}</span>
      <span class="ml-2 font-medium tabular-nums">{{ proxyItem?.shared ?? 0 }}</span>
    </p>
    <template #footer>
      <button type="button" class="btn btn-secondary btn-sm" @click="proxyItem = null">{{ t('admin.schedQuality.action.cancel') }}</button>
      <button type="button" class="btn btn-primary btn-sm" @click="confirmProxyClear">{{ t('admin.schedQuality.action.clear') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
/**
 * The account quality panel (spec 5, 7a, 7b; prototype). One instance per page, opened through useSchedLayers.
 *
 * - Content, top to bottom, with as few words as it can: the account's name with 豁免, 恢复 and close on one line; its
 *   automatic states, when there are any; the model tabs; the groups table with the last 12 hours (SchedPanelGroups),
 *   where a group's place opens its own exemption; the decision records (SchedDecisions).
 * - Data: the backend's own detail of the account (no compatibility layer) and its 12-hour figures, fetched by the host
 *   page's live table (useSchedLayerData); the content updates in place. After an exemption, its removal or a recovery
 *   from here markSchedChanged() tells the page, which loads the panel and its quality summary again.
 * - Switching to another account puts its name in the header at once; the body keeps the previous account's content,
 *   dimmed as the table dims its old rows, until the new detail lands. The skeleton shows only when the panel opens
 *   with nothing to show yet.
 * - Not modal: no backdrop, the table behind stays usable. Its top edge follows the top of the table's visible area
 *   (`anchor`), never above the page header, so the toolbar stays visible and no scrolling row shows above it; it
 *   covers the table's right-hand columns. Below 1024px it takes the whole screen (prototype), with a 44px close
 *   button, and is modal there (7l-2): focus goes to the title, Tab stays inside, the page behind neither scrolls nor
 *   takes focus. Crossing 1024px while open switches between the two.
 * - A pointerdown anywhere outside closes it and the click goes on as usual; on another element that opens the
 *   panel (data-quality-opener) the content switches instead; layers opened from the panel are not outside. Escape
 *   and the close button close it too and give focus back to the dot that opened it.
 */
import { computed, nextTick, onScopeDispose, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useEventListener, useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import SchedPanelGroups from './SchedPanelGroups.vue'
import SchedOverridePopover from './SchedOverridePopover.vue'
import SchedDecisions from './SchedDecisions.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendAutoState, BackendDetailGroup, SchedOverrideRequest } from '@/api/admin/schedQualityTypes'
import { SCHED_PANEL_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { useLiveClock } from '@/composables/useLiveClock'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { closeSchedDrawer, markSchedChanged, openSchedDrawer, qualityOpenerKey, useSchedLayers } from '@/composables/useSchedLayers'
import { isRecovering, useAccountRecovery } from '@/composables/useAccountRecovery'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { isReducedMotion } from '@/composables/useMotionPreference'
import { motionDuration, motionEase } from '@/utils/motionTokens'
import { kindName, parentIdFromKey, secondsUntil } from '@/utils/schedQualityFormat'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { vRovingTabs } from '@/utils/rovingTabs'
import { vTip } from '@/utils/tip'
import { allGroupsExemption, closesPanel, modelMarks, panelModels, panelRecoverable, pickModelTab } from './qualityPanel'

const props = defineProps<{
  /** Opens the IQ check records of an account; the link is hidden when not given. */
  onIqCheck?: (accountId: number) => void
  /** Opens the account edit dialog; the link is hidden when not given. */
  onEdit?: (accountId: number) => void
  /** Reads the account back on the page after a recovery from here. */
  onRecovered?: (accountId: number) => Promise<void> | void
  /** The page's row of an account (a parent's name before its detail is in), when the page has one. */
  account?: (id: number) => { name: string } | undefined
  /** Name and alias of a group by id, for the alias pills. */
  groupInfo?: (id: number) => { name: string; alias?: string | null } | undefined
  /** The element whose top edge the panel's top follows (the table); the page header's bottom without one. */
  anchor?: () => HTMLElement | null
}>()

const { t, te } = useI18n()
const { drawer } = useSchedLayers()
const { panel, retry } = useSchedLayerData()
// Write actions need accounts:manage; without it they are not shown.
const { canManage } = useAdminPermission('accounts')
const now = useLiveClock()
const titleId = `qp-title-${useId()}`
const rootRef = ref<HTMLElement | null>(null)
const titleRef = ref<HTMLElement | null>(null)
const bodyRef = ref<HTMLElement | null>(null)
const open = computed(() => drawer.accountId !== null)

// ---- Data ----
// The header is the open account's; the body shows the account the detail is for (the previous one right after a
// switch, until the new detail lands: `fresh` is false meanwhile).
const detail = computed(() => panel.detail)
const fresh = computed(() => panel.accountId !== null && panel.accountId === drawer.accountId)
const failed = computed(() => !fresh.value && drawer.accountId !== null && panel.failedId === drawer.accountId)
/** The previous account's content is shown while the new one is on its way (not once it failed). */
const switching = computed(() => !!detail.value && !fresh.value && !failed.value)

// ---- Layer: Escape, focus, outside clicks ----
const wide = useMediaQuery('(min-width: 1024px)')
const layer = useOverlayLayer(open, {
  root: rootRef,
  modal: () => !wide.value,
  initialFocus: () => titleRef.value,
  floor: 40,
  onEscape: () => close(true),
  // A dialog opened from the panel (the proxy confirmation, the account editor) covers it; the panel stays.
  onCovered: () => {}
})

// Keep focus in the panel when a refresh re-renders the control that had it.
let restoreFocus: (() => void) | null = null
watch(() => panel.detail, () => { restoreFocus = layer.holdFocus() }, { flush: 'pre' })
watch(() => panel.detail, () => { restoreFocus?.(); restoreFocus = null }, { flush: 'post' })

function focusOpener(accountId: number, groupId: number | null) {
  const usable = (el: HTMLElement) => el.isConnected && !el.closest('[inert]') && !el.matches(':disabled') && (el.checkVisibility?.() ?? true)
  const exact = Array.from(document.querySelectorAll<HTMLElement>(`[data-quality-opener="${qualityOpenerKey(accountId, groupId)}"]`))
  const any = Array.from(document.querySelectorAll<HTMLElement>(`[data-quality-opener^="${accountId}:"]`))
  ;[...exact, ...any].find(usable)?.focus({ preventScroll: true })
}

/** Escape and the close button give focus back to the opener; an outside click leaves it where the click puts it. */
function close(returnFocus: boolean) {
  const accountId = drawer.accountId
  const groupId = drawer.groupId
  popover.open = false
  closeSchedDrawer()
  if (returnFocus && accountId !== null) void nextTick(() => focusOpener(accountId, groupId))
}

useEventListener(document, 'pointerdown', (event: PointerEvent) => {
  if (open.value && closesPanel(event.target as Element | null, rootRef.value, layer.insideAbove)) close(false)
}, { capture: true })

// ---- Position ----
const top = ref(64)
const headerBottom = () => 4 * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16)
/** Below 1024px the panel takes the whole screen (prototype): nothing is left outside it to click. */
watch(wide, () => {
  if (open.value) place()
})
function place() {
  if (!wide.value) {
    top.value = 0
    return
  }
  const floor = headerBottom()
  const anchorTop = props.anchor?.()?.getBoundingClientRect().top
  top.value = Math.round(Math.max(floor, anchorTop ?? floor))
}
let frame = 0
const schedulePlace = () => {
  if (frame) return
  frame = requestAnimationFrame(() => { frame = 0; place() })
}
let observer: ResizeObserver | null = null
watch(open, (isOpen) => {
  observer?.disconnect()
  observer = null
  if (!isOpen) return
  place()
  const anchor = props.anchor?.()
  if (anchor && typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(schedulePlace)
    observer.observe(anchor)
  }
}, { immediate: true })
useEventListener(window, 'resize', () => { if (open.value) schedulePlace() })
useEventListener(window, 'scroll', () => { if (open.value) schedulePlace() }, { capture: true, passive: true })
onScopeDispose(() => {
  observer?.disconnect()
  if (frame) cancelAnimationFrame(frame)
})

// ---- Model tab and the target group ----
const models = computed(() => (detail.value ? panelModels(detail.value) : []))
const tabMarks = computed(() => (detail.value ? modelMarks(detail.value) : new Map<string, 'rear' | 'trial'>()))
const tab = ref<string | null>(null)
const targetGroupId = ref<number | null>(null)
/** The open request (a dot's group and model) is applied once its account's detail is shown. */
let pendingRequest = true

watch(() => [drawer.accountId, drawer.raise] as const, ([id]) => {
  pendingRequest = true
  if (id !== null && fresh.value) applyRequest(false)
})

// The small layers opened for one account (the exemption popover, the proxy confirmation) close when the account
// changes: confirming them would act on the new one (7h-2).
watch(() => drawer.accountId, () => {
  popover.open = false
  proxyItem.value = null
})

watch(detail, (value, previous) => {
  if (!value || !fresh.value) return
  const switched = previous?.account.id !== value.account.id
  if (switched) tab.value = null
  if (pendingRequest || switched) applyRequest(switched)
  else tab.value = pickModelTab(models.value, null, tab.value)
})

/** Opened from a dot: the group's substandard model's tab, and the group row brought into view. */
function applyRequest(toTop: boolean) {
  targetGroupId.value = drawer.groupId
  tab.value = pickModelTab(models.value, drawer.model, tab.value)
  pendingRequest = false
  void nextTick(() => {
    const body = bodyRef.value
    if (!body) return
    if (toTop) body.scrollTop = 0
    const target = targetGroupId.value === null ? null : body.querySelector<HTMLElement>(`[data-qp-group-row="${targetGroupId.value}"]`)
    if (!target) return
    const rowTop = target.offsetTop
    if (rowTop < body.scrollTop || rowTop + target.offsetHeight > body.scrollTop + body.clientHeight) body.scrollTop = Math.max(0, rowTop - 80)
  })
}

const groupInfo = (id: number) => props.groupInfo?.(id)
/** A group's name and alias for its pill: the groups store's, else the detail's name, else its id. */
const pillOf = (id: number) => groupInfo(id) ?? { name: detail.value?.groups.find((group) => group.group_id === id)?.name ?? `#${id}` }
const scopeGroups = computed(() => detail.value?.groups.map((group) => ({ id: group.group_id, name: group.name })) ?? [])

// ---- Exemptions ----
const allExemption = computed(() => (detail.value ? allGroupsExemption(detail.value) : null))
/**
 * One popover for the header's 豁免 (group 0) and a group's place: a new exemption, or with `undo` (its title, the
 * exemption in force) only its removal; `target` is the exemption it removes (0 for the all-groups one).
 */
const popover = reactive({ open: false, groupId: 0, anchor: null as HTMLElement | null, busy: false, failed: false, undo: null as string | null, target: 0 })
function openPopover(groupId: number, anchor: HTMLElement, undo: string | null = null, target = 0) {
  Object.assign(popover, { open: true, groupId, anchor, busy: false, failed: false, undo, target })
}
/** A group's place pressed: its exemption, or the removal of the one in force there. */
function openPlace(group: BackendDetailGroup, anchor: HTMLElement) {
  const exemption = group.exemption
  if (!exemption) return openPopover(group.group_id, anchor)
  const title = exemption.expires_at
    ? t('admin.schedQuality.panel.placeUntil.exempt', { time: formatShortTime(exemption.expires_at) })
    : t('admin.schedQuality.panel.exemptForever')
  openPopover(group.group_id, anchor, exemption.group_id === 0 ? t('admin.schedQuality.panel.exemptAll', { what: title }) : title, exemption.group_id)
}
function onHeaderExempt(event: MouseEvent) {
  if (allExemption.value) void clearExemption(0)
  else openPopover(0, event.currentTarget as HTMLElement)
}

async function applyExemption(request: SchedOverrideRequest) {
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

async function undoExemption() {
  const id = drawer.accountId
  if (id === null || !canManage.value) return
  popover.busy = true
  popover.failed = false
  try {
    await schedQualityAPI.clearOverride(id, popover.target)
    popover.open = false
    void markSchedChanged()
  } catch {
    popover.failed = true
  } finally {
    popover.busy = false
  }
}

async function clearExemption(groupId: number) {
  const id = drawer.accountId
  if (id === null) return
  try {
    await schedQualityAPI.clearOverride(id, groupId)
    void markSchedChanged()
  } catch {
    flash(failedGroups, groupId)
  }
}

// ---- 恢复 ----
const { recover } = useAccountRecovery()
const recoverable = computed(() => !!detail.value && panelRecoverable(detail.value))
const recovering = computed(() => !!detail.value && isRecovering(detail.value.account.id))
function startRecover() {
  const account = detail.value?.account
  if (account) void recover({ id: account.id, name: account.name }, () => props.onRecovered?.(account.id))
}

// ---- Automatic states: each model's own time ----
function scopeLabel(item: BackendAutoState) {
  if (item.scope === 'account') return t('admin.schedQuality.scope.account')
  if (item.scope === 'proxy') return t('admin.schedQuality.scope.proxy')
  return item.model || '—'
}

function autoValue(item: BackendAutoState) {
  // A clock time, not a countdown: "11:55" read as a time of day either way.
  if (secondsUntil(item.until, now.value)) {
    return t(item.kind === 'iq_gate' ? 'admin.schedQuality.panel.autoRecheck' : 'admin.schedQuality.panel.autoUntil', { time: formatShortTime(item.until) })
  }
  if (item.count) return `${item.count[0]} / ${item.count[1]}`
  return '—'
}

function gotoOf(item: BackendAutoState): { label: string; run: () => void } | null {
  const id = drawer.accountId
  if (id === null) return null
  if (item.kind === 'iq_gate' && props.onIqCheck) return { label: t('admin.schedQuality.goto.iq'), run: () => props.onIqCheck!(id) }
  if (item.kind === 'parent_blocked') {
    const parent = parentIdFromKey(item.key)
    // The button goes with the old content: focus moves to the title, which reads the new account's name (7h-3), the
    // parent's name the page knows until its detail is in (7l-9).
    return parent === null ? null : {
      label: t('admin.schedQuality.goto.parent'),
      run: () => { openSchedDrawer({ id: parent, name: props.account?.(parent)?.name }); titleRef.value?.focus() }
    }
  }
  if (['manual_off', 'expired', 'quota'].includes(item.kind) && props.onEdit) {
    return { label: t('admin.schedQuality.goto.edit'), run: () => props.onEdit!(id) }
  }
  return null
}

const proxyItem = shallowRef<BackendAutoState | null>(null)

async function runClear(item: BackendAutoState) {
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

function clearAuto(item: BackendAutoState) {
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

// Slides in from the right (--motion-panel, ease-out) and out in --motion-exit; at once under reduced motion. Only the
// transform moves.
let running: gsap.core.Tween | undefined
function onEnter(el: Element, done: () => void) {
  running?.kill()
  if (isReducedMotion()) return done()
  running = gsap.fromTo(el, { xPercent: 100 }, { xPercent: 0, duration: motionDuration('--motion-panel'), ease: motionEase('--ease-out'), clearProps: 'transform', onComplete: done })
}
function onLeave(el: Element, done: () => void) {
  running?.kill()
  if (isReducedMotion()) return done()
  running = gsap.to(el, { xPercent: 100, duration: motionDuration('--motion-exit'), ease: motionEase('--ease-in'), onComplete: done })
}
onScopeDispose(() => running?.kill())
</script>

<style scoped>
@reference "../../../../style.css";

.qpanel {
  position: fixed;
  right: 0;
  bottom: 0;
  display: grid;
  width: var(--sidebar-width);
  max-width: 100vw;
  grid-template-rows: auto minmax(0, 1fr);
  border-top: 1px solid var(--color-line-strong);
  border-left: 1px solid var(--color-line-strong);
  border-top-left-radius: var(--radius-lg);
  background: var(--color-bg-raised);
  box-shadow: var(--shadow-panel);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
}
.qpanel:focus {
  outline: none;
}

/* One line: the name takes what is left, then 豁免 and 恢复, then close. */
.qp-head {
  display: flex;
  min-height: 60px;
  align-items: center;
  gap: 12px;
  padding: 12px 12px 12px 20px;
  border-bottom: 1px solid var(--color-line-row);
}
.qp-name {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  font-size: var(--font-size-title);
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-title);
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* Focused when the panel switches to another account from inside (7h-3); the ring only for keyboard use. */
.qp-name:focus {
  outline: none;
}
.qp-name:focus-visible {
  border-radius: var(--radius-sm);
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.qp-close {
  display: grid;
  width: 32px;
  height: 32px;
  flex: none;
  place-items: center;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
}
.qp-close:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  .qp-close:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.qp-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
}

.qp-body {
  position: relative;
  display: grid;
  min-height: 0;
  align-content: start;
  gap: 16px;
  overflow-y: auto;
  overscroll-behavior: contain;
  /* A scrollbar that comes and goes (a record opened) must not shift the table's columns. */
  scrollbar-gutter: stable;
  padding: 16px 20px 20px;
}
.qp-loading {
  display: grid;
  gap: 14px;
}
.qp-failed {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.qp-failed__why {
  min-width: 0;
  flex: 1 1 12rem;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  overflow-wrap: anywhere;
}

/* Model tabs: chips with their whole names, wrapping onto another line when they do not fit. */
.qp-seg {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
/* A tab is never wider than the row: a very long name breaks inside its tab, whole. */
.qp-seg__btn {
  display: inline-flex;
  max-width: 100%;
  min-height: 26px;
  align-items: center;
  gap: 5px;
  padding: 2px 10px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  text-align: left;
  transition: background-color var(--motion-fast) var(--ease-out), color var(--motion-fast) var(--ease-out);
}
.qp-seg__label {
  min-width: 0;
  overflow-wrap: anywhere;
}
.qp-seg__btn:active {
  background: var(--color-bg-seg-active);
}
.qp-seg__btn[aria-selected='true'] {
  border-color: transparent;
  background: var(--color-bg-seg-active);
  color: var(--color-text-primary);
}
@media (hover: hover) {
  .qp-seg__btn:hover:not([aria-selected='true']) {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.qp-seg__btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.qp-seg__dot {
  display: block;
  width: 6px;
  height: 6px;
  flex: none;
  border-radius: 50%;
  background: var(--color-status-back);
}
.qp-seg__dot.is-trial {
  background: var(--color-status-info);
}

/* From 1280px: 40px more (the width stays as it was). */
@media (min-width: 1280px) {
  .qpanel {
    width: calc(var(--sidebar-width) + 40px);
  }
}
/* Below 1024px (prototype): the whole screen, an obvious 44px close button. */
@media (max-width: 1023px) {
  .qpanel {
    left: 0;
    width: auto;
    max-width: none;
    border: 0;
    border-radius: 0;
    box-shadow: none;
  }
  .qp-close {
    width: 44px;
    height: 44px;
  }
}
/* A phone (prototype, panel under 440px): tighter padding, the two header buttons side by side at full width. */
/* A phone: the name above, 豁免 and 恢复 under it at full width, close at the top right. */
@media (max-width: 439px) {
  .qp-head {
    flex-wrap: wrap;
    padding: 8px 4px 12px 16px;
  }
  .qp-actions {
    order: 3;
    width: 100%;
    padding-right: 12px;
  }
  .qp-body {
    padding: 16px 16px 32px;
  }
  .qp-actions .btn,
  .qp-actions [data-qp-recover] {
    height: 36px;
    flex: 1;
  }
}
/* Touch: every control here at least 40px to hit. The model tabs grow; the rest keep their look and reach out with an
   invisible area that stays inside its row. */
@media (pointer: coarse) {
  .qp-seg__btn {
    height: 40px;
  }
  .arow {
    min-height: 40px;
  }
  .qp-close,
  .qp-actions .btn,
  .qpanel :deep(.qp-text-btn) {
    position: relative;
  }
  .qp-close::after,
  .qp-actions .btn::after,
  .qpanel :deep(.qp-text-btn)::after {
    position: absolute;
    inset: -4px;
    content: '';
  }
  .qp-actions .btn::after {
    inset: -6px 0;
  }
  .qpanel :deep(.qp-text-btn)::after {
    inset: -8px -2px;
  }
}

.qp-section {
  display: grid;
  gap: 8px;
}
.qp-h {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-body);
}
.qp-h-note {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-regular);
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* Automatic states: kind, model or scope, time, action */
.alist {
  display: grid;
}
.arow {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 12px;
  min-height: 36px;
  border-top: 1px solid var(--color-line-row);
}
.arow:first-child {
  border-top: 0;
}
.arow > * {
  min-width: 0;
}
/* What stops the account, in the rear's amber. */
.arow__k {
  color: var(--color-status-back-text);
  font-weight: var(--font-weight-medium);
}
.arow__m {
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.arow__t {
  font-variant-numeric: tabular-nums;
  text-align: right;
  white-space: nowrap;
}
.arow__a {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
}
.arow__a:empty {
  display: none;
}

/* Shared by the groups table */
.qpanel :deep(.qp-text-btn) {
  height: 24px;
  margin: 0 -6px;
  padding: 0 6px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  white-space: nowrap;
}
@media (hover: hover) {
  .qpanel :deep(.qp-text-btn:hover) {
    background: var(--color-bg-control-hover);
  }
}
.qpanel :deep(.qp-text-btn:focus-visible) {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
.qpanel :deep(.qp-muted) {
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
}
.qpanel :deep(.qp-fail) {
  color: var(--color-status-error-text);
  font-size: var(--font-size-small);
}
.qpanel :deep(.qp-note) {
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
}
</style>
