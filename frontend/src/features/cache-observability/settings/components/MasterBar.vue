<template>
  <!-- The master switch heads the group of keys it gates, so "kill switch ∧ master ∧ own config" reads as structure (§11.7). -->
  <section class="rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6" aria-labelledby="cache-master-title" data-test="master-bar">
    <div class="flex flex-wrap items-start justify-between gap-6">
      <div class="min-w-0 max-w-2xl">
        <h2 id="cache-master-title" class="flex flex-wrap items-center gap-2 text-base font-bold text-gray-900 dark:text-white">
          {{ t('admin.cacheSettings.master.title') }}
          <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium" :class="tone.pill" data-test="master-state">
            <span class="h-1.5 w-1.5 rounded-full" :class="tone.dot" aria-hidden="true" />
            {{ codeLabel('states', masterState, t, te) }}
          </span>
        </h2>
        <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.master.description') }}</p>
        <!-- The master write changes three features at once, so its receipt stays visible like any card's. -->
        <PolicyReceipt :state="state" :policy-key="MASTER_KEY" :now="now" />
      </div>
      <div v-if="master" class="flex items-center gap-3">
        <span id="cache-master-switch-label" class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.cacheSettings.master.switchLabel') }}</span>
        <PolicySwitch
          large
          :model-value="masterOn"
          aria-labelledby="cache-master-switch-label"
          :disabled="!!state.preview.value"
          data-test="master-switch"
          @update:model-value="toggle"
        />
      </div>
      <p v-else class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.master.unavailable') }}</p>
    </div>

    <ol class="mt-4 flex flex-wrap items-center gap-x-2 gap-y-2 text-xs" :aria-label="t('admin.cacheSettings.chain.label')" data-test="effect-chain">
      <li v-for="(link, index) in chain" :key="link.key" class="flex items-center gap-2">
        <span v-if="index" class="text-gray-500 dark:text-dark-400" aria-hidden="true">{{ t('admin.cacheSettings.chain.and') }}</span>
        <span class="inline-flex items-center gap-1.5 rounded-lg bg-gray-50 px-2.5 py-1.5 text-gray-700 ring-1 ring-inset ring-gray-900/5 dark:bg-dark-900/60 dark:text-dark-200 dark:ring-dark-700">
          <span class="h-1.5 w-1.5 rounded-full" :class="link.dot" aria-hidden="true" />
          <span class="font-medium text-gray-900 dark:text-gray-100">{{ link.name }}</span>
          {{ link.value }}
        </span>
      </li>
    </ol>

    <dl class="mt-4 flex flex-wrap gap-x-6 gap-y-1 border-t border-gray-100 pt-4 text-xs text-gray-600 dark:border-dark-700 dark:text-dark-300" data-test="master-provenance">
      <div v-if="master" class="flex gap-1">
        <dt>{{ t('admin.cacheSettings.footer.rev') }}</dt>
        <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ master.stored.rev }}</dd>
      </div>
      <div v-if="master?.loaded" class="flex gap-1" data-test="master-loaded">
        <dt>{{ t('admin.cacheSettings.footer.loaded') }}</dt>
        <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ t('admin.cacheSettings.footer.loadedValue', { rev: master.loaded.rev, time: formatBoth(master.loaded.loaded_at, t) }) }}</dd>
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
    </dl>

    <div class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-700" data-test="gated-group">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.cacheSettings.master.gatedHeading') }}</h3>
      <div class="mt-3 space-y-4 border-l-2 pl-4 sm:pl-5" :class="masterOn && !state.killSwitch.value ? 'border-primary-300 dark:border-primary-700' : 'border-gray-200 dark:border-dark-600'">
        <slot />
        <!-- Keys this build does not ship fold into one line: no card, no risk tag (m-2, §11.7). -->
        <p v-if="missing.length" class="flex items-start gap-2 text-sm text-gray-600 dark:text-dark-300" data-test="gated-not-included">
          <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
          {{ t('admin.cacheSettings.notIncluded', { names: missing.join(t('admin.cacheSettings.nameSeparator')) }) }}
        </p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { MASTER_KEY } from '../catalog'
import { codeLabel, formatBoth, formatUtc, relativeTime, stateTone } from '../format'
import type { PoliciesState } from '../usePolicies'
import PolicyReceipt from './PolicyReceipt.vue'
import PolicySwitch from './PolicySwitch.vue'

const props = defineProps<{ state: PoliciesState; now: Date; missing: string[] }>()
const { t, te } = useI18n()
const master = computed(() => props.state.master.value)
const masterOn = computed(() => props.state.masterOn.value)
// The badge follows master's own effective state (parse_error, killed, pending_reload…), not the stored flag (m-3).
const masterState = computed(() => props.state.stateOf(MASTER_KEY))
const tone = computed(() => stateTone(masterState.value))

const chain = computed(() => {
  const kill = props.state.killSwitch.value
  return [
    {
      key: 'kill',
      name: t('admin.cacheSettings.chain.kill'),
      value: kill ? t('admin.cacheSettings.chain.killOn') : t('admin.cacheSettings.chain.killOff'),
      dot: kill ? 'bg-red-500' : 'bg-emerald-500',
    },
    {
      key: 'master',
      name: t('admin.cacheSettings.chain.master'),
      value: masterOn.value ? t('admin.cacheSettings.values.on') : t('admin.cacheSettings.values.off'),
      dot: masterOn.value ? 'bg-emerald-500' : 'bg-gray-400 dark:bg-dark-400',
    },
    { key: 'own', name: t('admin.cacheSettings.chain.own'), value: t('admin.cacheSettings.chain.ownValue'), dot: 'bg-gray-400 dark:bg-dark-400' },
  ]
})

function toggle(next: boolean) {
  const m = master.value
  if (m) void props.state.openPreview(MASTER_KEY, { ...m.stored.spec, enabled: next })
}
</script>
