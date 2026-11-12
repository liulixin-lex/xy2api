<template>
  <span v-if="loading" class="skeleton inline-block h-3.5 w-10 align-middle" />
  <span v-else-if="!summary" />
  <span v-else class="flex flex-nowrap items-center gap-1.5 max-md:flex-wrap max-md:justify-end" data-sq-groups>
    <template v-if="summary.cooling.length">
      <button
        v-for="item in summary.cooling"
        :key="item.group_id"
        type="button"
        v-tip="truncated[item.group_id] ? groupName(item.group_id) : ''"
        :class="['sq-capsule sq-focus', item.testing ? 'sq-capsule--test' : 'sq-capsule--cool', { 'sq-capsule--new': isNew(item) }]"
        :data-sq-trigger="`cap:${summary.account_id}:${item.group_id}`"
        @click.stop="emit('open', { model: item.model, groupId: item.group_id })"
      >
        <span :ref="(el) => measure(item.group_id, el)" class="sq-capsule__name">{{ groupName(item.group_id) }}</span>
        <span class="sq-capsule__state">{{ t(item.testing ? 'admin.schedQuality.state.testing' : 'admin.schedQuality.state.cooling') }}</span>
      </button>
      <button
        v-if="summary.cooling_more > 0"
        type="button"
        class="sq-plain sq-focus sq-text-3 text-xs"
        :data-sq-trigger="`grp:${summary.account_id}`"
        @click.stop="emit('open', { model: summary.model, toGroups: true })"
      >{{ t('admin.schedQuality.more', { n: summary.cooling_more }) }}</button>
    </template>
    <button
      v-else-if="summary.groups.total === summary.groups.nodata"
      type="button"
      class="sq-plain sq-focus sq-text-3 text-sm"
      :data-sq-trigger="`grp:${summary.account_id}`"
      @click.stop="emit('open', { model: summary.model, toGroups: true })"
    >{{ t('admin.schedQuality.state.nodata') }}</button>
    <button
      v-else
      type="button"
      class="sq-plain sq-focus inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300"
      :data-sq-trigger="`grp:${summary.account_id}`"
      @click.stop="emit('open', { model: summary.model, toGroups: true })"
    ><span class="sq-dot" aria-hidden="true" />{{ t('admin.schedQuality.state.normal') }}</button>
  </span>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary, SchedAccountSummaryCooling } from '@/types/schedQuality'
import { vTip } from '@/utils/tip'

// Groups column of the account list (design spec A.3): capsules `{group} 冷却` / `{group} 测试中` (at most 3, then +N),
// otherwise `正常` with a dot or `暂无数据`. Kept on one line so row heights stay stable for the virtualizer.
const props = defineProps<{
  summary?: SchedAccountSummary
  loading?: boolean
  groupName: (id: number) => string
  /** Capsule keys (`account:group:state`) that newly appeared on this refresh and get the one-time emphasis. */
  fresh?: Set<string>
}>()
const emit = defineEmits<{ open: [target: { model: string | null; groupId?: number; toGroups?: boolean }] }>()
const { t } = useI18n()

const capsuleKey = (item: SchedAccountSummaryCooling) =>
  `${props.summary?.account_id}:${item.group_id}:${item.testing ? 'testing' : 'cooling'}`
const isNew = (item: SchedAccountSummaryCooling) => !!props.fresh?.has(capsuleKey(item))

// A capsule whose group name is cut off shows the full name in a tip.
const truncated = reactive<Record<number, boolean>>({})
function measure(groupId: number, el: unknown) {
  if (!(el instanceof HTMLElement)) return
  requestAnimationFrame(() => {
    const cut = el.scrollWidth > el.clientWidth
    if (truncated[groupId] !== cut) truncated[groupId] = cut
  })
}
</script>
