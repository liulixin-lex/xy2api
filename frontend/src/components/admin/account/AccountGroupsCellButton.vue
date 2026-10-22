<template>
  <button
    type="button"
    data-testid="account-groups-cell"
    class="-mx-1 flex max-w-56 flex-wrap items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
    :aria-label="t('admin.groupCategories.editAccountGroupsAria', { name: accountName })"
    :title="groups.length ? groups.map((group) => group.name).join('、') : undefined"
    @click="emit('open')"
  >
    <template v-if="groups.length">
      <GroupBadge
        v-for="group in visibleGroups"
        :key="group.id"
        :name="group.name"
        :platform="group.platform"
        :subscription-type="group.subscription_type"
        :rate-multiplier="group.rate_multiplier"
        :show-rate="false"
        class="max-w-24"
      />
      <span v-if="hiddenCount > 0" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
    </template>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { Group } from '@/types'

// 账号列表的分组单元格：整格是一个按钮，点开“编辑分组”弹窗；没有分组时显示“—”也能点
const MAX_VISIBLE = 3

const props = defineProps<{
  groups: Group[]
  accountName: string
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()

const visibleGroups = computed(() => props.groups.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, props.groups.length - MAX_VISIBLE))
</script>
