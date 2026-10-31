<template>
  <button
    type="button"
    data-testid="account-groups-cell"
    class="-mx-1 flex max-w-56 flex-wrap items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
    :aria-label="t('admin.groupCategories.editAccountGroupsAria', { name: accountName })"
    :aria-describedby="qualitySummary ? summaryId : undefined"
    :title="groups.length ? groups.map((group) => group.name).join('、') : undefined"
    @click="emit('open')"
  >
    <template v-if="groups.length">
      <template v-for="group in visibleGroups" :key="group.id">
        <GroupQualityBadge
          v-if="quality?.get(group.id)"
          :name="group.name"
          :quality="quality.get(group.id)!"
          :metrics="metrics"
        />
        <GroupBadge
          v-else
          :name="group.name"
          :platform="group.platform"
          :subscription-type="group.subscription_type"
          :rate-multiplier="group.rate_multiplier"
          :show-rate="false"
          class="max-w-24"
        />
      </template>
      <span v-if="hiddenCount > 0" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
    </template>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
    <span v-if="qualitySummary" :id="summaryId" class="sr-only">{{ qualitySummary }}</span>
  </button>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupQualityBadge from './GroupQualityBadge.vue'
import { formatDateTime } from '@/utils/format'
import type { Group } from '@/types'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import { QUALITY_CLOCK, groupQualityStateLabel, orderGroupsByQuality, type AccountGroupQuality } from './groupQualityPresentation'

// 账号列表的分组单元格：整格是一个按钮，点开“编辑分组”弹窗；没有分组时显示“—”也能点。
// 开启质量门槛的分组按状态着色，有问题的排在前面。
const MAX_VISIBLE = 3

const props = defineProps<{
  groups: Group[]
  accountName: string
  /** groupId → 该账号在开启门槛的分组里的状态 */
  quality?: Map<number, AccountGroupQuality>
  metrics?: GroupQualityAccountMetrics
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()
const summaryId = useId()

const orderedGroups = computed(() => orderGroupsByQuality(props.groups, props.quality))
const visibleGroups = computed(() => orderedGroups.value.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, props.groups.length - MAX_VISIBLE))

// 按钮的可访问名称是“编辑分组”，门槛状态（含被 +N 收起的分组）作为补充描述读出。
const qualitySummary = computed(() => {
  const clock = (iso: string) => formatDateTime(iso, QUALITY_CLOCK)
  return orderedGroups.value
    .filter((group) => props.quality?.has(group.id))
    .map((group) => t('admin.groupQualityGate.tooltip.state', {
      group: group.name,
      state: groupQualityStateLabel(props.quality!.get(group.id)!, t, clock)
    }))
    .join(t('admin.groupQualityGate.listSeparator'))
})
</script>
