<template>
  <div class="pills" :class="{ 'pills--center': groups.length <= 3, 'pills--card': card }" :data-groups-cell="account.id" data-test="group-pills">
    <button
      v-if="!groups.length"
      type="button"
      class="pill pill--empty"
      :disabled="readonly"
      :aria-label="t('admin.accounts.table.pillEmptyLabel', { name: account.name })"
      v-tip="quietTip('empty', () => t('admin.accounts.table.pillEmpty'))"
      data-test="group-pill-empty"
      :data-groups-opener="account.id"
      @click="hush('empty'); emit('edit-groups', $event)"
    >
      <Icon name="plus" size="xs" />
    </button>

    <template v-for="pill in shownPills" :key="pill.group.id">
      <!-- Touch (spec 4.3): the whole pill opens the quality panel on its group; a pill with no dot has nothing to open. -->
      <button
        v-if="coarse && pill.state"
        type="button"
        class="pill pill--whole"
        :data-group-id="pill.group.id"
        :aria-label="t('admin.accounts.table.pillDotLabel', { name: pill.group.name, state: stateLabel(pill.state) })"
        :data-quality-opener="qualityOpenerKey(account.id, pill.group.id)"
        data-test="group-pill"
        @click="emit('open-quality', pill.group.id)"
      >
        <span class="pill__dot"><StatusDot :tone="DOT_TONES[pill.state]" /></span>
        <span class="pill__text" :class="{ 'pill__text--latin': pill.latin }">{{ pill.text }}</span>
      </button>
      <!-- No dot, nothing to open: a tap shows the group's full name, as "+N" lists what it folds (7l-5). -->
      <button
        v-else-if="coarse"
        type="button"
        class="pill pill--nodot pill--whole"
        :data-group-id="pill.group.id"
        :aria-label="t('admin.accounts.table.pillNoQuality', { name: pill.group.name })"
        v-tip="t('admin.accounts.table.pillNoQuality', { name: pill.group.name })"
        data-test="group-pill"
        @click="showTipNow($event.currentTarget)"
      >
        <span class="pill__text" :class="{ 'pill__text--latin': pill.latin }">{{ pill.text }}</span>
      </button>
      <span
        v-else
        class="pill"
        :class="{ 'pill--nodot': !pill.state }"
        :data-group-id="pill.group.id"
        data-test="group-pill"
      >
        <button
          v-if="pill.state"
          type="button"
          class="pill__dot"
          :aria-label="t('admin.accounts.table.pillDotLabel', { name: pill.group.name, state: stateLabel(pill.state) })"
          v-tip="quietTip(`dot:${pill.group.id}`, () => pillTip(pill.group, pill.state, 'dot'))"
          :data-quality-opener="qualityOpenerKey(account.id, pill.group.id)"
          data-test="group-pill-dot"
          @click="hush(`dot:${pill.group.id}`); emit('open-quality', pill.group.id)"
        >
          <StatusDot :tone="DOT_TONES[pill.state]" />
        </button>
        <button
          type="button"
          class="pill__text"
          :class="{ 'pill__text--latin': pill.latin }"
          :disabled="readonly"
          :aria-label="t('admin.accounts.table.pillTextLabel', { name: pill.group.name })"
          v-tip="quietTip(`text:${pill.group.id}`, () => pillTip(pill.group, pill.state, 'text'))"
          data-test="group-pill-text"
          :data-groups-opener="account.id"
          @click="hush(`text:${pill.group.id}`); emit('edit-groups', $event)"
        >{{ pill.text }}</button>
      </span>
    </template>

    <!-- Touch: the whole "+N" opens the worst folded group in the quality panel, or lists them without quality data. -->
    <button
      v-if="rest.length && coarse"
      type="button"
      class="pill pill--more pill--whole"
      :class="{ 'pill--nodot': !restWorst }"
      :aria-label="restWorst ? t('admin.accounts.table.pillMoreWorst', { n: rest.length, state: stateLabel(restWorst.state) }) : t('admin.accounts.table.pillMoreLabel', { n: rest.length })"
      :data-quality-opener="restWorst ? qualityOpenerKey(account.id, restWorst.group.id) : undefined"
      data-test="group-pill-more"
      @click="restWorst ? emit('open-quality', restWorst.group.id) : showTipNow($event.currentTarget)"
      v-tip="moreTip"
    >
      <span v-if="restWorst" class="pill__dot"><StatusDot :tone="DOT_TONES[restWorst.state]" /></span>
      <span class="pill__text">+{{ rest.length }}</span>
    </button>
    <span v-else-if="rest.length" class="pill pill--more" :class="{ 'pill--nodot': !restWorst }" data-test="group-pill-more">
      <button
        v-if="restWorst"
        type="button"
        class="pill__dot"
        :aria-label="t('admin.accounts.table.pillMoreWorst', { n: rest.length, state: stateLabel(restWorst.state) })"
        v-tip="quietTip('more', moreTip)"
        :data-quality-opener="qualityOpenerKey(account.id, restWorst.group.id)"
        @click="hush('more'); emit('open-quality', restWorst.group.id)"
      >
        <StatusDot :tone="DOT_TONES[restWorst.state]" />
      </button>
      <button
        type="button"
        class="pill__text"
        :aria-label="t('admin.accounts.table.pillMoreLabel', { n: rest.length })"
        v-tip="moreTip"
        data-test="group-pill-more-text"
        @click="showTipNow($event.currentTarget)"
      >+{{ rest.length }}</button>
    </span>
  </div>
</template>

<script setup lang="ts">
/**
 * Groups column (spec 4.3, prototype): a fixed 3×3 grid of pills in the groups page's order. One to three pills sit in
 * one row, centred; more fill from the top. With more than nine the ninth is "+N" with the worst folded state; hovering
 * or clicking its text lists every folded group (spec 4.3, 7d-7), its dot opens the worst one. Each pill has two hit
 * zones: the dot opens the quality panel on that group, the text edits the account's groups. Non-OpenAI accounts have
 * no quality data and no dot.
 *
 * On a touch screen (coarse pointer, whatever the width) the whole pill opens the quality panel on its group, "+N" on
 * the worst folded one; editing goes through the card's and the row menu's "edit groups". In the card's grid each
 * pill's hit area is 40px high, the 8px gaps keeping neighbours' areas apart.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { iqModelLabel } from '@/components/admin/account/iqCheckPresentation'
import { useLiveClock } from '@/composables/useLiveClock'
import { usePointerCoarse } from '@/composables/usePointerCoarse'
import { qualityOpenerKey } from '@/composables/useSchedLayers'
import type { Account, AdminGroup } from '@/types'
import { anyOverlayOpen } from '@/utils/overlayStack'
import { hideTip, showTipNow, vTip } from '@/utils/tip'
import { groupPillText, isLatinPillText } from '@/utils/groupAlias'
import { groupDotState, splitPills, worstDotState, type AccountQualityView, type GroupDotState } from './accountGroups'
import { formatRemaining, formatShortTime, msLeft } from './accountTime'

const props = defineProps<{
  account: Account
  /** The account's groups, already in the groups page's order. */
  groups: AdminGroup[]
  /** The account's quality summary; null for accounts without quality data. */
  quality: AccountQualityView | null
  readonly?: boolean
  /** The narrow card's grid (prototype): 72×32 pills, 8px apart, the same zones and order. */
  card?: boolean
}>()
const emit = defineEmits<{ 'open-quality': [groupId: number]; 'edit-groups': [event: MouseEvent] }>()
const { t } = useI18n()
const now = useLiveClock()
const coarse = usePointerCoarse()

const DOT_TONES: Record<GroupDotState, StatusDotTone> = { ok: 'ok', back: 'back', exempt: 'exempt', observe: 'observe', stopped: 'off' }
const STATE_KEYS: Record<GroupDotState, string> = { ok: 'ok', back: 'back', exempt: 'exempt', observe: 'observe', stopped: 'stopped' }
const stateLabel = (state: GroupDotState) => t(`admin.accounts.table.dotStates.${STATE_KEYS[state]}`)

const stateOf = (group: AdminGroup): GroupDotState | null => (props.quality ? groupDotState(props.quality, group.id) : null)

const split = computed(() => splitPills(props.groups))
const shownPills = computed(() =>
  split.value.shown.map((group) => {
    const text = groupPillText(group)
    return { group, text, latin: isLatinPillText(text), state: stateOf(group) }
  })
)
const rest = computed(() => split.value.rest)
const restWorst = computed(() => {
  if (!props.quality) return null
  const states = rest.value.map((group) => ({ group, state: stateOf(group)! }))
  const worst = worstDotState(states.map((item) => item.state))
  return worst ? states.find((item) => item.state === worst)! : null
})

/** The state's own detail: substandard models with their reasons and time left, the exemption's end, … */
function stateDetail(group: AdminGroup, state: GroupDotState): string {
  const quality = props.quality
  if (!quality) return ''
  if (state === 'back') {
    const items = quality.substandard.filter((item) => item.group_id === group.id)
    const models = items
      .map((item) => `${iqModelLabel(item.model)} ${item.dims.map((dim) => t(`admin.accounts.table.dimReasons.${dim}`)).join('、')}`)
      .join('；')
    const until = items.map((item) => item.until).filter(Boolean).sort().pop()
    const left = msLeft(until, now.value)
    return [models, left === null ? '' : t('admin.accounts.table.pillBackUntil', { time: formatRemaining(t, left) })].filter(Boolean).join(' · ')
  }
  if (state === 'exempt') {
    const exemption = quality.exemptions.find((item) => item.group_id === group.id) ?? quality.exemptions.find((item) => item.group_id === 0)
    return exemption?.expires_at ? t('admin.accounts.table.pillExemptUntil', { time: formatShortTime(exemption.expires_at) }) : t('admin.accounts.table.pillExemptForever')
  }
  if (state === 'observe') return t('admin.accounts.table.pillObserve')
  if (state === 'stopped') return t('admin.accounts.table.pillStopped')
  return ''
}

/**
 * A click on a hit zone that opens the quality panel or the group picker puts its tip away at once, and the tip stays
 * away while that layer is open, so the first Escape closes the layer (7i-3). Other zones' tips show as usual.
 */
let quiet: string | null = null
const hush = (zone: string) => {
  quiet = zone
  hideTip()
}
const quietTip = (zone: string, text: () => string) => () => (quiet === zone && anyOverlayOpen() ? '' : text())

function pillTip(group: AdminGroup, state: GroupDotState | null, zone: 'dot' | 'text'): string {
  const head = state
    ? [t('admin.accounts.table.pillTip', { name: group.name, state: stateLabel(state) }), stateDetail(group, state)].filter(Boolean).join(' · ')
    : t('admin.accounts.table.pillNoQuality', { name: group.name })
  if (zone === 'text' && props.readonly) return head
  return `${head}\n${t(zone === 'dot' ? 'admin.accounts.table.pillDotAction' : 'admin.accounts.table.pillTextAction')}`
}

function moreTip(): string {
  const worst = restWorst.value
  const head = worst
    ? t('admin.accounts.table.pillMoreWorst', { n: rest.value.length, state: stateLabel(worst.state) })
    : t('admin.accounts.table.pillMore', { n: rest.value.length })
  const lines = rest.value.map((group) => {
    const state = stateOf(group)
    return [`${groupPillText(group)}  ${group.name}`, state ? stateLabel(state) : '', state ? stateDetail(group, state) : ''].filter(Boolean).join(' · ')
  })
  return [head, ...lines].join('\n')
}
</script>

<style scoped>
.pills {
  display: grid;
  height: 66px;
  grid-template-columns: repeat(3, var(--pill-width));
  grid-auto-rows: var(--pill-height);
  gap: var(--pill-gap-y) var(--pill-gap-x);
  align-content: start;
}
.pills--center {
  align-content: center;
}
.pills--card {
  --pill-width: 72px;
  --pill-height: 32px;
  --pill-dot-zone: 24px;
  --pill-gap-x: 8px;
  --pill-gap-y: 8px;
  height: auto;
  align-content: start;
}
.pills--card .pill,
.pills--card .pill__text {
  font-size: var(--font-size-body);
}
.pills--card .pill__text--latin {
  font-size: var(--font-size-small);
}
.pill {
  display: flex;
  width: var(--pill-width);
  height: var(--pill-height);
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  line-height: var(--pill-height);
}
.pill__dot,
.pill__text {
  height: 100%;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  transition: background-color var(--motion-fast) var(--ease-out);
}
.pill__dot {
  display: grid;
  flex: none;
  width: var(--pill-dot-zone);
  place-items: center;
  border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  cursor: pointer;
}
.pill__dot :deep(.status-dot) {
  transition: transform var(--motion-fast) var(--ease-out);
}
.pill__text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  padding-right: 4px;
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  font-size: var(--font-size-small);
  text-align: left;
  text-overflow: clip;
  white-space: nowrap;
  cursor: pointer;
}
.pill__text:disabled {
  cursor: default;
}
.pill__text--latin {
  font-size: var(--font-size-micro);
}
.pill__dot:focus-visible,
.pill__text:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  .pill__dot:hover {
    background: var(--color-bg-pill-hover);
  }
  .pill__dot:hover :deep(.status-dot) {
    transform: scale(1.3);
  }
  .pill__text:hover:not(:disabled) {
    background: var(--color-bg-pill-hover);
  }
}
.pill--nodot .pill__text {
  padding: 0;
  border-radius: var(--radius-sm);
  text-align: center;
}
.pill--more .pill__text {
  color: var(--color-text-secondary);
}
/* Touch: one button over the whole pill; the text still cuts inside it. */
.pill--whole {
  position: relative;
  border: 0;
  padding: 0;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}
.pill--whole:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.pills--card .pill--whole {
  overflow: visible;
}
.pills--card .pill--whole::after {
  position: absolute;
  inset: -4px 0;
  content: '';
}
.pill--empty {
  display: grid;
  place-items: center;
  padding: 0;
  border: 1px dashed var(--color-line-control);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.pill--empty:disabled {
  cursor: default;
}
@media (hover: hover) {
  .pill--empty:hover:not(:disabled) {
    border-color: var(--color-text-muted);
    background: var(--color-bg-pill);
    color: var(--color-text-primary);
  }
}
</style>
