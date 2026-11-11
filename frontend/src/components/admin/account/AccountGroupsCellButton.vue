<template>
  <button
    type="button"
    data-testid="account-groups-cell"
    class="-mx-1 flex min-w-0 max-w-56 flex-wrap items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
    :aria-label="t('admin.groupCategories.editAccountGroupsAria', { name: accountName })"
    :title="groups.length ? groups.map((group) => group.name).join('、') : undefined"
    @click="emit('open')"
  >
    <template v-if="groups.length">
      <!-- “+N”跟最后一个标签放在一起，换行时不会单独落到一行 -->
      <span v-for="(group, index) in visibleGroups" :key="group.id" class="inline-flex min-w-0 max-w-full items-center gap-1">
        <GroupBadge
          :name="group.name"
          :platform="group.platform"
          :show-platform-icon="group.platform !== accountPlatform"
          :subscription-type="group.subscription_type"
          :rate-multiplier="group.rate_multiplier"
          :show-rate="false"
          :title="group.name"
          class="max-w-40"
        />
        <span v-if="hiddenCount > 0 && index === visibleGroups.length - 1" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
      </span>
    </template>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { Group } from '@/types'

// 账号列表的分组单元格：整格是一个按钮，点开“编辑分组”；没有分组时显示“—”也能点。
const props = defineProps<{
  groups: Group[]
  accountName: string
  /** 账号平台；分组平台与之相同时不再重复显示平台图标 */
  accountPlatform?: string
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()

// 最多显示 2 个标签，名称尽量完整，其余收进 +N（按钮 title 列出全部分组）。
const MAX_VISIBLE = 2
const visibleGroups = computed(() => props.groups.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, props.groups.length - MAX_VISIBLE))
</script>
