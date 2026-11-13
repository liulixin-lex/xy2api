<template>
  <p v-if="!groups.length" class="sq-text-3 text-sm">—</p>
  <div v-else class="-mx-3 overflow-x-auto">
    <table class="sq-table min-w-[620px]">
      <thead>
        <tr>
          <th scope="col">{{ t('admin.schedQuality.col.group') }}</th>
          <th scope="col">{{ t('admin.schedQuality.col.state') }}</th>
          <th v-for="dim in SCHED_DIMS" :key="dim" scope="col" class="sq-r w-[76px]">{{ t(`admin.schedQuality.dim.${dim}`) }}</th>
          <th scope="col" class="sq-r w-[64px]">{{ t('admin.schedQuality.col.until') }}</th>
          <th scope="col" class="sq-r w-[48px]">{{ t('admin.schedQuality.col.level') }}</th>
          <th scope="col" class="sq-r w-[88px]"><span class="sr-only">{{ t('admin.accounts.columns.actions') }}</span></th>
        </tr>
      </thead>
      <tbody v-for="group in groups" :key="group.group_id" :class="{ 'sq-target': group.group_id === targetGroupId }" :data-sq-group-row="group.group_id">
        <!-- The group: its name, exemption and three lines, each over the column it applies to. -->
        <tr class="sq-group-head">
          <td>
            <div class="flex min-w-0 items-center gap-1.5">
              <button v-if="canViewGroups" type="button" class="sq-link sq-focus min-w-0 truncate" :data-sq-trigger="`order:${group.group_id}`" @click="emit('order', group)">{{ group.name }}</button>
              <span v-else class="min-w-0 truncate font-medium">{{ group.name }}</span>
              <SchedOverrideCapsule v-if="group.exemption" class="shrink-0" :all-groups="group.exemption.group_id === 0" />
            </div>
          </td>
          <td class="sq-text-3">{{ group.policy.enabled ? '' : t('admin.schedQuality.policy.off') }}</td>
          <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num sq-text-3" :data-sq-line="dim">{{ policyParts(group.policy)[dim] }}</td>
          <td />
          <td />
          <td class="sq-r">
            <span v-if="failedGroups.has(group.group_id)" class="sq-fail-word mr-2">{{ t('admin.schedQuality.result.failed') }}</span>
            <button
              v-if="canManage"
              type="button"
              class="btn btn-secondary sq-btn-xs sq-focus"
              :data-sq-trigger="`override:${group.group_id}`"
              @click="onAction(group, $event)"
            >{{ t(group.exemption ? 'admin.schedQuality.action.undoExempt' : 'admin.schedQuality.action.exempt') }}</button>
          </td>
        </tr>
        <!-- One row per model: the evidence of this group, its verdict colour, recovery time and level. -->
        <tr v-for="row in group.by_model" :key="row.model" :data-sq-model-row="`${group.group_id}:${row.model}`">
          <td class="pl-6 sq-text-2">{{ row.model }}</td>
          <td>
            <span :class="stateOf(row).cls" data-sq-state>{{ stateOf(row).text }}</span>
          </td>
          <td v-for="dim in SCHED_DIMS" :key="dim" class="sq-r sq-num" :data-sq-dim="dim">
            <template v-for="cell in [dimDetail(row, dim)]" :key="cell.text">
              <span :class="cell.cls">{{ cell.text }}</span><sup v-if="cell.merged" v-tip="t('admin.schedQuality.tip.merged')" class="ml-0.5 sq-text-3" data-sq-merged>{{ t('admin.schedQuality.mark.merged') }}</sup>
              <span v-if="cell.n !== null" class="block text-[11px] sq-text-3">{{ t('admin.schedQuality.samples', { n: cell.n }) }}</span>
            </template>
          </td>
          <td class="sq-r sq-num">
            <template v-if="row.failing && row.until">
              <span>{{ fmtAt(row.until, now) }}</span><sup v-if="row.other_group_ids.length" v-tip="t('admin.schedQuality.tip.otherGroup')" class="ml-0.5 sq-text-3" data-sq-other>{{ t('admin.schedQuality.mark.other') }}</sup>
            </template>
            <span v-else class="sq-text-3">—</span>
          </td>
          <td class="sq-r sq-num">{{ row.level ? t('admin.schedQuality.level', { n: row.level }) : '—' }}</td>
          <td />
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import SchedOverrideCapsule from './SchedOverrideCapsule.vue'
import type { SchedAccountGroupDetail, SchedGroupModelDetail } from '@/types/schedQuality'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { dimDetail, fmtAt, policyParts, SCHED_DIMS } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// Groups of the account drawer (FINAL §7): per group its three lines and exemption, then one row per model with the
// group's own evidence, the verdict colour, the recovery time and the ladder level.
defineProps<{
  groups: SchedAccountGroupDetail[]
  now: number
  targetGroupId?: number | null
  failedGroups: Set<number>
}>()
const emit = defineEmits<{
  order: [group: SchedAccountGroupDetail]
  override: [group: SchedAccountGroupDetail, anchor: HTMLElement]
  undo: [group: SchedAccountGroupDetail]
}>()
const { t } = useI18n()
// The group link needs groups:view (plain name otherwise); the row button needs accounts:manage.
const { canView: canViewGroups } = useAdminPermission('groups')
const { canManage } = useAdminPermission('accounts')

function stateOf(row: SchedGroupModelDetail): { text: string; cls: string } {
  if (row.failing && !row.exempt) {
    const dims = row.dims.map((dim) => t(`admin.schedQuality.dim.${dim}`))
    return { text: [t('admin.schedQuality.state.substandard'), ...dims].join('·'), cls: 'sq-cool' }
  }
  return { text: t('admin.schedQuality.state.normal'), cls: 'sq-text' }
}

function onAction(group: SchedAccountGroupDetail, event: MouseEvent) {
  if (group.exemption) emit('undo', group)
  else emit('override', group, event.currentTarget as HTMLElement)
}
</script>
