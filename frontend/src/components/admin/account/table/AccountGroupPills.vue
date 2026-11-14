<template>
  <div class="pills" :class="{ 'pills--center': groups.length <= 3 }" data-test="group-pills">
    <button
      v-if="!groups.length"
      type="button"
      class="pill pill--empty"
      :disabled="readonly"
      :aria-label="t('admin.accounts.table.pillEmptyLabel', { name: account.name })"
      v-tip="t('admin.accounts.table.pillEmpty')"
      data-test="group-pill-empty"
      :data-groups-opener="account.id"
      @click="emit('edit-groups', $event)"
    >
      <Icon name="plus" size="xs" />
    </button>

    <span
      v-for="pill in shownPills"
      :key="pill.group.id"
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
        v-tip="() => pillTip(pill.group, pill.state, 'dot')"
        :data-quality-opener="qualityOpenerKey(account.id, pill.group.id)"
        data-test="group-pill-dot"
        @click="emit('open-quality', pill.group.id)"
      >
        <StatusDot :tone="DOT_TONES[pill.state]" />
      </button>
      <button
        type="button"
        class="pill__text"
        :class="{ 'pill__text--latin': pill.latin }"
        :disabled="readonly"
        :aria-label="t('admin.accounts.table.pillTextLabel', { name: pill.group.name })"
        v-tip="() => pillTip(pill.group, pill.state, 'text')"
        data-test="group-pill-text"
        :data-groups-opener="account.id"
        @click="emit('edit-groups', $event)"
      >{{ pill.text }}</button>
    </span>

    <span v-if="rest.length" class="pill pill--more" :class="{ 'pill--nodot': !restWorst }" data-test="group-pill-more">
      <button
        v-if="restWorst"
        type="button"
        class="pill__dot"
        :aria-label="t('admin.accounts.table.pillMoreWorst', { n: rest.length, state: stateLabel(restWorst.state) })"
        v-tip="moreTip"
        :data-quality-opener="qualityOpenerKey(account.id, restWorst.group.id)"
        @click="emit('open-quality', restWorst.group.id)"
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
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { iqModelLabel } from '@/components/admin/account/iqCheckPresentation'
import { useLiveClock } from '@/composables/useLiveClock'
import { qualityOpenerKey } from '@/composables/useSchedLayers'
import type { Account, AdminGroup } from '@/types'
import { showTipNow, vTip } from '@/utils/tip'
import { groupDotState, groupPillText, isLatinPillText, splitPills, worstDotState, type AccountQualityView, type GroupDotState } from './accountGroups'
import { formatRemaining, formatShortTime, msLeft } from './accountTime'

const props = defineProps<{
  account: Account
  /** The account's groups, already in the groups page's order. */
  groups: AdminGroup[]
  /** The account's quality summary; null for accounts without quality data. */
  quality: AccountQualityView | null
  /** Quality scheduling is switched on and its view fresh; otherwise no group shows at the back. */
  qualityActive?: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{ 'open-quality': [groupId: number]; 'edit-groups': [event: MouseEvent] }>()
const { t } = useI18n()
const now = useLiveClock()

const DOT_TONES: Record<GroupDotState, StatusDotTone> = { ok: 'ok', back: 'back', exempt: 'exempt', observe: 'observe', stopped: 'off' }
const STATE_KEYS: Record<GroupDotState, string> = { ok: 'ok', back: 'back', exempt: 'exempt', observe: 'observe', stopped: 'stopped' }
const stateLabel = (state: GroupDotState) => t(`admin.accounts.table.dotStates.${STATE_KEYS[state]}`)

const stateOf = (group: AdminGroup): GroupDotState | null => (props.quality ? groupDotState(props.quality, group.id, props.qualityActive !== false) : null)

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
