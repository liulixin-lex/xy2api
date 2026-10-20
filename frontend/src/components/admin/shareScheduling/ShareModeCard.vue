<template>
  <section class="card p-5" data-testid="share-mode">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div class="min-w-0">
        <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white" data-testid="share-group-title">{{ view.group_name }}</h2>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
          {{ view.platform }} ·
          {{ view.version > 0 && view.updated_at ? t('admin.shareScheduling.mode.updated', { version: view.version, time: formatDateTime(view.updated_at) }) : t('admin.shareScheduling.mode.neverSaved') }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button
          v-if="view.scheduler === 'share'"
          type="button"
          class="btn btn-secondary"
          :disabled="busy"
          data-testid="share-rollback"
          @click="store.requestChange({ scheduler: 'sub2api' })"
        >{{ t('admin.shareScheduling.mode.rollback') }}</button>
        <button
          v-else
          type="button"
          class="btn btn-primary"
          :disabled="busy"
          data-testid="share-switch"
          @click="store.requestChange({ scheduler: 'share' })"
        >{{ t('admin.shareScheduling.mode.switchToShare') }}</button>
      </div>
    </div>

    <div class="mt-4 grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <div class="rounded-xl border border-gray-200 p-3 dark:border-dark-600">
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.mode.title') }}</p>
        <div class="mt-1.5 flex items-center gap-2">
          <span
            class="shrink-0 whitespace-nowrap rounded-md px-2 py-0.5 text-sm font-semibold"
            :class="view.scheduler === 'share' ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'"
            data-testid="share-mode-current"
          >{{ labels.scheduler(view.scheduler) }}</span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.schedulerHints.' + view.scheduler) }}</span>
        </div>
      </div>
      <div class="flex items-start justify-between gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-600">
        <div class="min-w-0">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.mode.sessionKeep') }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.mode.sessionKeepHint') }}</p>
        </div>
        <Toggle
          :model-value="view.session_keep"
          :aria-label="t('admin.shareScheduling.mode.sessionKeep')"
          data-testid="share-session-keep"
          :class="{ 'pointer-events-none opacity-50': busy }"
          @update:model-value="value => !busy && store.requestChange({ session_keep: value })"
        />
      </div>
    </div>

    <p v-if="view.invalid" class="mt-3 rounded-xl bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert" data-testid="share-invalid">
      {{ t('admin.shareScheduling.mode.invalid', { reason: view.invalid }) }}
    </p>
    <p v-if="view.legacy_shadow" class="mt-3 rounded-xl bg-sky-50 px-3 py-2 text-sm text-sky-800 dark:bg-sky-900/20 dark:text-sky-200" data-testid="share-legacy-shadow">
      {{ t('admin.shareScheduling.mode.legacyShadow') }}
    </p>
    <p v-if="store.globalError !== null" class="mt-3 rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" role="alert" data-testid="share-global-failed">
      {{ t('admin.shareScheduling.mode.globalFailed') }}
      <span v-if="store.globalError" class="block text-xs">{{ store.globalError }}</span>
    </p>
    <p v-else-if="view.scheduler === 'share' && !view.effective" class="mt-3 rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="share-not-effective">
      {{ t('admin.shareScheduling.mode.notEffective') }}
    </p>
    <div v-if="store.conflict" class="mt-3 flex flex-wrap items-center gap-3 rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" role="alert" data-testid="share-conflict">
      <span class="min-w-0 flex-1">{{ t('admin.shareScheduling.mode.conflict') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" data-testid="share-conflict-reload" @click="store.reload()">{{ t('admin.shareScheduling.mode.reload') }}</button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { SharePolicyView } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { formatDateTime } from '@/utils/format'

defineProps<{ view: SharePolicyView }>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const store = useShareSchedulingStore()
const busy = computed(() => store.saving || store.previewing || store.pending !== null)
</script>
