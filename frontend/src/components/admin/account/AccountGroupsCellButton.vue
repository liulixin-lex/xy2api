<template>
  <component
    :is="readonly ? 'span' : 'button'"
    v-tip="readonly ? '' : t('admin.schedQuality.tip.editMembership')"
    v-bind="readonly ? {} : { type: 'button', 'aria-label': t('admin.groupCategories.editAccountGroupsAria', { name: accountName }) }"
    data-testid="account-groups-cell"
    :class="[
      '-mx-1 flex min-w-0 max-w-56 flex-wrap items-center gap-1.5 rounded-md px-1 py-0.5 text-left',
      readonly ? '' : 'sq-focus transition-colors hover:bg-gray-100 dark:hover:bg-dark-700'
    ]"
    @click="!readonly && emit('open')"
  >
    <template v-if="groups.length">
      <!-- “+N”跟最后一个标签放在一起，换行时不会单独落到一行 -->
      <span v-for="(group, index) in visibleGroups" :key="group.id" class="inline-flex min-w-0 max-w-full items-center gap-1.5">
        <GroupBadge
          :name="group.name"
          :platform="group.platform"
          :show-platform-icon="false"
          :subscription-type="group.subscription_type"
          :rate-multiplier="group.rate_multiplier"
          :show-rate="false"
          class="max-w-[8.5em] leading-[18px]"
        />
        <span v-if="hiddenCount > 0 && index === visibleGroups.length - 1" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
      </span>
    </template>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import { vTip } from '@/utils/tip'
import type { Group } from '@/types'

// 账号列表的归组单元格：整格是一个按钮，点开“编辑分组”；没有分组时显示“—”也能点。只读时是静态徽章：
// 没有编辑提示、编辑读屏名称与 Tab 停留点（场景 36）。
// 徽章按分组平台着色、不显示平台图标（颜色已区分平台，ui-proposal 第 3 节）。
const props = defineProps<{
  groups: Group[]
  accountName: string
  readonly?: boolean
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()

// 最多显示 2 个标签，其余收进 +N，在点开的编辑弹窗里看全部分组。
const MAX_VISIBLE = 2
const visibleGroups = computed(() => props.groups.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, props.groups.length - MAX_VISIBLE))
</script>
