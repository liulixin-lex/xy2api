<template>
  <section class="space-y-4" aria-labelledby="cache-master-title" data-test="master-bar">
    <div
      v-if="state.killSwitch.value"
      role="alert"
      class="flex items-start gap-3 rounded-3xl border border-red-200 bg-red-50 px-5 py-4 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200"
      data-test="kill-switch-banner"
    >
      <Icon name="ban" size="md" class="mt-0.5 shrink-0" aria-hidden="true" />
      <div>
        <p class="font-semibold">{{ t('admin.cacheSettings.kill.title') }}</p>
        <p class="mt-0.5">{{ t('admin.cacheSettings.kill.description') }}</p>
      </div>
    </div>

    <div class="rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-6">
        <div class="min-w-0 max-w-2xl">
          <h2 id="cache-master-title" class="flex flex-wrap items-center gap-2 text-base font-bold text-gray-900 dark:text-white">
            {{ t('admin.cacheSettings.master.title') }}
            <span class="badge" :class="masterOn ? 'badge-success' : 'badge-gray'" data-test="master-state">
              {{ masterOn ? t('admin.cacheSettings.values.on') : t('admin.cacheSettings.values.off') }}
            </span>
          </h2>
          <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.master.description') }}</p>
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.master.controls') }}</p>
        </div>
        <div v-if="master && master.available" class="flex items-center gap-3">
          <span id="cache-master-switch-label" class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.cacheSettings.master.switchLabel') }}</span>
          <button
            type="button"
            role="switch"
            :aria-checked="masterOn"
            aria-labelledby="cache-master-switch-label"
            class="relative inline-flex h-8 w-14 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 dark:focus-visible:ring-offset-dark-800"
            :class="masterOn ? 'bg-primary-600' : 'bg-gray-300 dark:bg-dark-600'"
            :disabled="state.killSwitch.value || !!state.preview.value"
            data-test="master-switch"
            @click="state.openPreview('master', { ...master.stored.spec, enabled: !masterOn })"
          >
            <span
              class="pointer-events-none inline-block h-7 w-7 transform rounded-full bg-white shadow ring-0 transition duration-150 motion-reduce:transition-none"
              :class="masterOn ? 'translate-x-6' : 'translate-x-0'"
            />
          </button>
        </div>
        <p v-else class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.master.unavailable') }}</p>
      </div>

      <dl class="mt-4 flex flex-wrap gap-x-6 gap-y-1 border-t border-gray-100 pt-4 text-xs text-gray-600 dark:border-dark-700 dark:text-dark-300">
        <div v-if="master" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.footer.rev') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ master.stored.rev }}</dd>
        </div>
        <div v-if="master?.stored.updated_at" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.footer.updatedBy') }}</dt>
          <dd class="text-gray-900 dark:text-gray-100">
            {{ master.stored.updated_by?.email || t('admin.cacheSettings.footer.unknownActor') }},
            <time :datetime="master.stored.updated_at" :title="formatUtc(master.stored.updated_at)">{{ relativeTime(master.stored.updated_at, now, t) }}</time>
          </dd>
        </div>
        <div v-if="master?.stored.reason" class="flex min-w-0 gap-1">
          <dt>{{ t('admin.cacheSettings.footer.reason') }}</dt>
          <dd class="min-w-0 break-words text-gray-900 dark:text-gray-100">{{ master.stored.reason }}</dd>
        </div>
        <div v-if="data?.process_started_at" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.master.processStarted') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatUtc(data.process_started_at) }}</dd>
        </div>
        <div v-if="data?.build_version" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.master.build') }}</dt>
          <dd class="text-gray-900 dark:text-gray-100">{{ data.build_version }}</dd>
        </div>
        <div class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.master.refresh') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ t('admin.cacheSettings.master.refreshValue', { seconds: state.refreshSeconds.value }) }}</dd>
        </div>
      </dl>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { formatUtc, relativeTime } from '../format'
import type { PoliciesState } from '../usePolicies'

const props = defineProps<{ state: PoliciesState; now: Date }>()
const { t } = useI18n()
const data = computed(() => props.state.data.value)
const master = computed(() => props.state.master.value)
const masterOn = computed(() => props.state.masterOn.value)
</script>
