<template>
  <p v-if="!detail.groups.length" class="qp-note" data-test="qp-no-groups">{{ t('admin.schedQuality.panel.noGroups') }}</p>
  <div v-else class="grows" role="table" :aria-label="t('admin.schedQuality.drawer.byGroup')">
    <div class="grow grow--head" role="row">
      <span role="columnheader">{{ t('admin.schedQuality.col.group') }}</span>
      <span role="columnheader">{{ t('admin.schedQuality.col.place') }}</span>
      <span v-for="dim in RECENT_DIMS" :key="dim" role="columnheader" class="grow__c">{{ t(`admin.schedQuality.dim.${dim}`) }}</span>
      <span role="columnheader" class="grow__r">{{ t('admin.accounts.columns.actions') }}</span>
    </div>
    <template v-for="item in rows" :key="item.group.group_id">
      <div
        class="grow"
        :class="{ 'is-target': item.group.group_id === targetGroupId, 'is-open': expanded.has(item.group.group_id) }"
        role="row"
        :data-qp-group-row="item.group.group_id"
      >
        <span class="grow__group" role="cell">
          <button
            type="button"
            class="grow__toggle"
            :aria-expanded="expanded.has(item.group.group_id)"
            :aria-controls="`${detailIdPrefix}-${item.group.group_id}`"
            :aria-label="t('admin.schedQuality.panel.evidenceToggle', { name: item.group.name })"
            :data-qp-expand="item.group.group_id"
            @click="toggle(item.group.group_id)"
          >
            <Icon name="chevronRight" size="xs" :stroke-width="2.5" />
          </button>
          <GroupAliasPill :group="item.info" />
          <span class="grow__name" :title="item.group.name">{{ item.group.name }}</span>
        </span>
        <span class="grow__state" role="cell" data-test="qp-state" :data-qp-place="item.place">
          <StatusDot :tone="PLACE_TONES[item.place]" />
          <span :class="item.place === 'front' ? 'status-text-normal' : ''">{{ word('placement', item.place) }}</span>
          <span v-if="item.extra" class="grow__extra">{{ item.extra }}</span>
        </span>
        <span class="grow__marks" role="none">
          <span
            v-for="mark in item.marks"
            :key="mark.dim"
            v-tip="mark.tip"
            class="mk"
            :class="`mk--${mark.identity}`"
            role="cell"
            tabindex="0"
            :aria-label="mark.tip"
            :data-qp-mark="mark.dim"
            :data-identity="mark.identity"
          >
            <span class="mk__l" aria-hidden="true">{{ t(`admin.schedQuality.dim.${mark.dim}`) }}</span>
            <Icon v-if="mark.identity === 'pass'" name="check" size="xs" :stroke-width="3" />
            <Icon v-else-if="mark.identity === 'rear'" name="x" size="xs" :stroke-width="3" />
            <Icon v-else-if="mark.identity === 'trial'" name="clock" size="xs" :stroke-width="3" />
            <span v-else class="mk__dash" aria-hidden="true" />
          </span>
        </span>
        <span class="grow__ops" role="cell">
          <span v-if="failedGroups.has(item.group.group_id)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
          <template v-if="!canManage || item.place === 'stopped'"><span class="qp-muted">—</span></template>
          <span v-else-if="item.group.exemption?.group_id === 0" class="qp-muted">{{ t('admin.schedQuality.panel.allExempt') }}</span>
          <button
            v-else-if="item.group.exemption"
            type="button"
            class="qp-text-btn"
            :data-qp-trigger="`unexempt:${item.group.group_id}`"
            @click="emit('unexempt', item.group)"
          >{{ t('admin.schedQuality.action.undoExempt') }}</button>
          <button
            v-else
            type="button"
            class="qp-text-btn"
            :data-qp-trigger="`exempt:${item.group.group_id}`"
            @click="emit('exempt', item.group, $event.currentTarget as HTMLElement)"
          >{{ t('admin.schedQuality.action.exempt') }}</button>
        </span>
      </div>
      <div v-if="expanded.has(item.group.group_id)" :id="`${detailIdPrefix}-${item.group.group_id}`" class="gdet" role="row" :data-qp-group-detail="item.group.group_id">
        <div role="cell" class="gdet__cell">
          <p v-if="!item.row" class="qp-note">{{ t('admin.schedQuality.panel.noData', { why: item.unknownWhy }) }}</p>
          <div v-for="mark in item.marks" :key="mark.dim" class="gdet__line" :data-qp-line="mark.dim">
            <div class="gdet__head">
              <span class="gdet__k">{{ t(`admin.schedQuality.dim.${mark.dim}`) }}</span>
              <span class="idw" :class="`idw--${mark.identity}`">{{ mark.word }}</span>
              <span v-if="mark.status" class="gdet__t">{{ mark.status }}</span>
            </div>
            <SchedEvidence
              v-if="item.row"
              :dim="mark.dim"
              :evidence="item.row.lines[mark.dim].evidence"
              :source-groups="item.row.lines[mark.dim].source_groups"
              :group-name="groupName"
              :gate-off="item.gateOff !== null"
            />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The quality panel's "各分组" table: per group the alias pill and full name, the place the three lines add up to
 * for the selected model (front, trial, rear; or exempt, stopped), each line's identity (pass, unknown in grey with
 * "证据不够", trial with the time left, rear with when it ends) and the exemption action. A row opens to each line's
 * evidence. On a phone (under 440px) each group takes two lines, the pill and action over the place and the marks,
 * and each mark carries its line's name.
 */
import { computed, reactive, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import type { BackendAccountDetail, BackendDetailGroup, BackendLine } from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { RECENT_DIMS } from '@/components/admin/account/table/accountRecent'
import { fmtCountdown, schedWord } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'
import SchedEvidence from './SchedEvidence.vue'
import { gateOff, groupPlace, lineIdentity, lineSecondsLeft, modelRow, type PanelPlace } from './qualityPanel'

const props = defineProps<{
  detail: BackendAccountDetail
  model: string | null
  now: number
  targetGroupId: number | null
  /** Name and alias of a group by id (the groups store); the detail's name when unknown. */
  groupInfo: (id: number) => { name: string; alias?: string | null } | undefined
  canManage: boolean
  failedGroups: Set<number>
}>()
const emit = defineEmits<{ exempt: [group: BackendDetailGroup, anchor: HTMLElement]; unexempt: [group: BackendDetailGroup] }>()
const { t, te } = useI18n()
const word = (table: string, value: string) => schedWord(table, value, t, te)
const detailIdPrefix = `qp-gdet-${useId()}`

const PLACE_TONES: Record<PanelPlace, StatusDotTone> = { front: 'ok', trial: 'info', rear: 'back', exempt: 'exempt', stopped: 'off' }
/** Rear stay of each level, minutes. */
const STAY_MINUTES = [20, 40, 60, 90, 120]

// Open rows stay open across refreshes; another account starts closed.
const expanded = reactive(new Set<number>())
watch(() => props.detail.account.id, () => expanded.clear())
function toggle(groupId: number) {
  if (expanded.has(groupId)) expanded.delete(groupId)
  else expanded.add(groupId)
}

const groupName = (id: number) => props.groupInfo(id)?.name ?? props.detail.groups.find((group) => group.group_id === id)?.name

/**
 * What follows the identity word: why it is unknown (the gate switched off for everything or for this group, else not
 * enough evidence), the trial's time left, the rear stay and its end.
 */
function lineStatus(line: BackendLine | undefined, left: number | null, unknownWhy: string): string {
  const identity = line?.identity ?? 'unknown'
  if (identity === 'unknown') return unknownWhy
  if (identity === 'trial') return left === null ? '' : t('admin.schedQuality.identity.trialLeft', { time: fmtCountdown(left) })
  if (identity === 'rear' && line) {
    const minutes = STAY_MINUTES[Math.min(Math.max(line.level, 0), STAY_MINUTES.length - 1)]
    return line.until
      ? t('admin.schedQuality.identity.rearUntil', { time: formatShortTime(line.until), minutes })
      : t('admin.schedQuality.identity.rearStay', { minutes })
  }
  return ''
}

const rows = computed(() =>
  props.detail.groups.map((group) => {
    const place = groupPlace(props.detail, group, props.model)
    const row = modelRow(group, props.model)
    const off = gateOff(props.detail, group)
    const unknownWhy = t(off === 'global' ? 'admin.schedQuality.identity.gateOffGlobal' : off === 'group' ? 'admin.schedQuality.identity.gateOffGroup' : 'admin.schedQuality.identity.unknownWhy')
    const marks = RECENT_DIMS.map((dim) => {
      const line = row?.lines[dim]
      const identity = lineIdentity(row, dim)
      const left = lineSecondsLeft(line, props.now)
      const status = lineStatus(line, left, unknownWhy)
      const name = word('identity', identity)
      return { dim, identity, left, status, word: name, tip: [t(`admin.schedQuality.dim.${dim}`), name, status].filter(Boolean).join(' · ') }
    })
    let extra = ''
    if (place === 'rear' || place === 'trial') {
      // The place lasts as long as its longest line.
      const lefts = marks.filter((mark) => mark.identity === place && mark.left !== null).map((mark) => mark.left as number)
      extra = lefts.length ? fmtCountdown(Math.max(...lefts)) : ''
    } else if (place === 'exempt' && group.exemption) {
      extra = group.exemption.expires_at
        ? t('admin.schedQuality.panel.exemptUntil', { time: formatShortTime(group.exemption.expires_at) })
        : t('admin.schedQuality.panel.exemptForever')
    }
    return { group, row, info: props.groupInfo(group.group_id) ?? { name: group.name }, place, extra, marks, unknownWhy, gateOff: off }
  })
)
</script>

<style scoped>
.grows {
  display: grid;
}
.grow {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 96px 32px 32px 32px 72px;
  align-items: center;
  column-gap: 4px;
  min-height: 40px;
  margin: 0 -8px;
  padding: 0 8px;
  border-top: 1px solid var(--color-line-row);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
}
.grow > * {
  min-width: 0;
}
.grow--head {
  min-height: 28px;
  border-top: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.grow__c {
  text-align: center;
}
.grow__r {
  text-align: right;
}
.grow.is-target {
  border-top-color: transparent;
  background: var(--color-bg-target);
}
.grow.is-target + .grow {
  border-top-color: transparent;
}
.grow__group {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}
.grow__toggle {
  display: grid;
  width: 20px;
  height: 20px;
  flex: none;
  place-items: center;
  margin-left: -4px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
}
.grow__toggle svg {
  transition: transform var(--motion-fast) ease-out;
}
.grow__toggle[aria-expanded='true'] svg {
  transform: rotate(90deg);
}
.grow__toggle:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (prefers-reduced-motion: reduce) {
  .grow__toggle svg {
    transition: none;
  }
}
/* A row's evidence: under the row, the three lines one after another. */
.gdet {
  margin: 0 -8px;
  padding: 4px 8px 12px 32px;
}
.gdet__cell {
  display: grid;
  gap: 10px;
}
.gdet__line {
  display: grid;
  gap: 4px;
}
.gdet__head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 8px;
}
.gdet__k {
  min-width: 32px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.gdet__t {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
/* Identity words: pass green, rear red, trial blue, unknown grey. */
.idw {
  font-weight: var(--font-weight-medium);
}
.idw--pass {
  color: var(--color-mark-pass);
}
.idw--rear {
  color: var(--color-mark-fail);
}
.idw--trial {
  color: var(--color-status-info);
}
.idw--unknown {
  color: var(--color-text-muted);
}
.grow__name {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.grow__state {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.grow__extra {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
.mk {
  display: flex;
  height: 24px;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-sm);
}
.mk:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
/* Pass, rear and trial: a 16px filled circle, green, red or blue, with a white check, cross or clock drawn in its
   middle 10px (12.8). Unknown is a grey dash. */
.mk--pass svg,
.mk--rear svg,
.mk--trial svg {
  box-sizing: border-box;
  width: 16px;
  height: 16px;
  padding: 3px;
  border-radius: 50%;
  color: var(--color-mark-glyph);
}
.mk--pass svg {
  background: var(--color-mark-pass);
}
.mk--rear svg {
  background: var(--color-mark-fail);
}
.mk--trial svg {
  background: var(--color-status-info);
}
.mk--unknown {
  color: var(--color-text-muted);
}
.mk__dash {
  display: block;
  width: 9px;
  height: 1.5px;
  border-radius: 1px;
  background: currentColor;
}
.grow__ops {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
  white-space: nowrap;
}
.grow__marks {
  display: contents;
}
.mk__l {
  display: none;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}

/* A phone (prototype): two lines per group, the header row gone, each mark named. */
@media (max-width: 439px) {
  .grow--head {
    display: none;
  }
  .grow {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas: 'group ops' 'state marks';
    row-gap: 6px;
    padding: 10px 8px;
  }
  .grow__group {
    grid-area: group;
  }
  .grow__state {
    grid-area: state;
  }
  .grow__ops {
    grid-area: ops;
  }
  .grow__marks {
    display: flex;
    grid-area: marks;
    justify-content: flex-end;
    gap: 10px;
  }
  .mk {
    gap: 4px;
  }
  .mk__l {
    display: block;
  }
}
</style>
