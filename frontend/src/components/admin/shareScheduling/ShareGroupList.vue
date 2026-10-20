<template>
  <nav class="card flex min-h-0 flex-col p-4" :aria-label="t('admin.shareScheduling.groups.title')">
    <div class="flex items-center justify-between gap-2">
      <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.groups.title') }}</h2>
      <span class="text-xs text-gray-500 dark:text-dark-400">{{ groups.length }}</span>
    </div>
    <input v-model.trim="query" type="search" class="input mt-3" :placeholder="t('admin.shareScheduling.groups.search')" :aria-label="t('admin.shareScheduling.groups.search')" data-testid="share-group-search">
    <div v-if="loading && !groups.length" class="mt-3 space-y-2" role="status" :aria-label="t('common.loading')">
      <div v-for="row in 4" :key="row" class="h-14 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-700" />
    </div>
    <div v-else-if="failed" class="mt-3 space-y-2" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.shareScheduling.groups.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="emit('retry')">{{ t('admin.shareScheduling.groups.retry') }}</button>
    </div>
    <p v-else-if="!groups.length" class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.groups.empty') }}</p>
    <p v-else-if="!sections.some(s => s.groups.length)" class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.groups.noMatch') }}</p>
    <div v-else class="mt-3 max-h-[70vh] space-y-4 overflow-y-auto pr-1">
      <section v-for="section in sections" v-show="section.groups.length" :key="section.key" :data-testid="'share-group-section-' + section.key">
        <h3 class="px-1 text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.groups.' + section.key) }} · {{ section.groups.length }}</h3>
        <ul class="mt-1 space-y-1">
          <li v-for="group in section.groups" :key="group.group_id">
            <button
              type="button"
              class="w-full rounded-xl px-3 py-2 text-left transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
              :class="group.group_id === selected ? 'bg-primary-50 ring-1 ring-primary-200 dark:bg-primary-900/20 dark:ring-primary-800' : 'hover:bg-gray-50 dark:hover:bg-dark-700'"
              :aria-current="group.group_id === selected ? 'true' : undefined"
              :data-testid="'share-group-' + group.group_id"
              @click="emit('select', group.group_id)"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="min-w-0 truncate font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
                <span v-if="group.scheduler === 'share'" class="shrink-0 rounded-md px-1.5 py-0.5 text-[11px] font-medium" :class="group.effective ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'">
                  {{ group.effective ? labels.scheduler('share') : t('admin.shareScheduling.groups.notEffective') }}
                </span>
              </div>
              <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-gray-500 dark:text-dark-400">
                <span>{{ group.platform }}</span>
                <span>{{ t('admin.shareScheduling.groups.members', { count: group.member_count }) }}</span>
                <span v-if="group.cache_hit_ratio_1h !== null">{{ t('admin.shareScheduling.groups.cacheHit', { value: formatSharePercent(group.cache_hit_ratio_1h) }) }}</span>
                <span v-if="group.switch_loss_ratio_1h">{{ t('admin.shareScheduling.groups.switchLoss', { value: formatSharePercent(group.switch_loss_ratio_1h, 2) }) }}</span>
                <span v-if="group.invalid" class="font-medium text-red-600 dark:text-red-400">{{ t('admin.shareScheduling.groups.invalid') }}</span>
              </div>
            </button>
          </li>
        </ul>
      </section>
    </div>
  </nav>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ShareGroupSummary } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { formatSharePercent } from '@/utils/shareScheduling'

const props = defineProps<{
  groups: ShareGroupSummary[]
  selected: number | null
  loading: boolean
  failed: boolean
}>()
const emit = defineEmits<{
  (e: 'select', groupId: number): void
  (e: 'retry'): void
}>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const query = ref('')

const sections = computed(() => {
  const q = query.value.toLowerCase()
  const visible = q ? props.groups.filter(g => `${g.name} ${g.group_id} ${g.platform}`.toLowerCase().includes(q)) : props.groups
  return [
    { key: 'active', groups: visible.filter(g => g.scheduler === 'share') },
    { key: 'idle', groups: visible.filter(g => g.scheduler !== 'share') }
  ]
})
</script>
