<template>
  <p v-if="!detail.groups.length" class="qp-note" data-test="qp-no-groups">{{ t('admin.schedQuality.panel.noGroups') }}</p>
  <div v-else class="grows" role="table" :aria-label="t('admin.schedQuality.drawer.byGroup')">
    <div class="grow grow--head" role="row">
      <span role="columnheader">{{ t('admin.schedQuality.col.group') }}</span>
      <span role="columnheader">{{ t('admin.schedQuality.col.state') }}</span>
      <span v-for="dim in RECENT_DIMS" :key="dim" role="columnheader" class="grow__c">{{ t(`admin.schedQuality.dim.${dim}`) }}</span>
      <span role="columnheader" class="grow__r">{{ t('admin.accounts.columns.actions') }}</span>
    </div>
    <div
      v-for="item in rows"
      :key="item.group.group_id"
      class="grow"
      :class="{ 'is-target': item.group.group_id === targetGroupId }"
      role="row"
      :data-qp-group-row="item.group.group_id"
    >
      <span class="grow__group" role="cell">
        <GroupAliasPill :group="item.info" />
        <span class="grow__name" :title="item.group.name">{{ item.group.name }}</span>
      </span>
      <span class="grow__state" role="cell" data-test="qp-state">
        <StatusDot :tone="DOT_TONES[item.state]" />
        <span :class="item.state === 'ok' ? 'status-text-normal' : ''">{{ stateWord(item.state) }}</span>
        <span v-if="item.extra" class="grow__extra">{{ item.extra }}</span>
      </span>
      <span class="grow__marks" role="none">
        <span
          v-for="mark in item.marks"
          :key="mark.dim"
          v-tip="mark.tip"
          class="mk"
          :class="`mk--${mark.mark}`"
          role="cell"
          tabindex="0"
          :aria-label="mark.tip"
          :data-qp-mark="mark.dim"
        >
          <span class="mk__l" aria-hidden="true">{{ t(`admin.schedQuality.dim.${mark.dim}`) }}</span>
          <Icon v-if="mark.mark === 'pass'" name="check" size="xs" :stroke-width="3" />
          <Icon v-else-if="mark.mark === 'fail'" name="x" size="xs" :stroke-width="3" />
          <span v-else class="mk__dash" aria-hidden="true" />
        </span>
      </span>
      <span class="grow__ops" role="cell">
        <span v-if="failedGroups.has(item.group.group_id)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
        <template v-if="!canManage || item.state === 'stopped'"><span class="qp-muted">—</span></template>
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
  </div>
</template>

<script setup lang="ts">
/**
 * The quality panel's "各分组" table (prototype): per group the alias pill and full name, the state for the selected
 * model (dot and word; a back row counts down to its end, an exemption shows until when), the backend's verdict of
 * each dimension as passed / failed / too few samples, and the exemption action. On a phone (under 440px) each group
 * takes two lines, the pill and action over the state and the marks, and each mark carries its dimension's name.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import type { BackendAccountDetail, BackendDetailGroup } from '@/api/admin/schedQualityTypes'
import type { GroupDotState } from '@/components/admin/account/table/accountGroups'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { RECENT_DIMS } from '@/components/admin/account/table/accountRecent'
import { fmtCountdown, secondsUntil } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'
import { dimMark, groupModelState, modelRow } from './qualityPanel'

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
const { t } = useI18n()

const DOT_TONES: Record<GroupDotState, StatusDotTone> = { ok: 'ok', back: 'back', exempt: 'exempt', observe: 'observe', stopped: 'off' }
const stateWord = (state: GroupDotState) => t(`admin.accounts.table.dotStates.${state}`)
const MARK_WORDS = { pass: 'pass', fail: 'fail', short: 'short' } as const

const rows = computed(() =>
  props.detail.groups.map((group) => {
    const state = groupModelState(props.detail, group, props.model)
    const row = modelRow(group, props.model)
    let extra = ''
    if (state === 'back') {
      const left = secondsUntil(row?.until ?? null, props.now)
      extra = left ? fmtCountdown(left) : ''
    } else if (state === 'exempt' && group.exemption) {
      extra = group.exemption.expires_at
        ? t('admin.schedQuality.panel.exemptUntil', { time: formatShortTime(group.exemption.expires_at) })
        : t('admin.schedQuality.panel.exemptForever')
    }
    const marks = RECENT_DIMS.map((dim) => {
      const mark = dimMark(row, dim)
      const word = t(`admin.schedQuality.panel.marks.${MARK_WORDS[mark]}`)
      const samples = row ? t('admin.schedQuality.tip.samples', { n: row[dim].n }) : ''
      return { dim, mark, tip: [`${t(`admin.schedQuality.dim.${dim}`)} · ${word}`, samples].filter(Boolean).join(' · ') }
    })
    return { group, info: props.groupInfo(group.group_id) ?? { name: group.name }, state, extra, marks }
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
/* Pass and fail: a 16px filled circle, green or red, with a white check or cross drawn in its middle 10px (12.8). */
.mk--pass svg,
.mk--fail svg {
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
.mk--fail svg {
  background: var(--color-mark-fail);
}
.mk--short {
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
