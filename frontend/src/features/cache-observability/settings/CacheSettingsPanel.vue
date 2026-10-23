<template>
  <div class="space-y-6" data-test="cache-settings-panel">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <p class="max-w-3xl text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.intro', { seconds: state.refreshSeconds.value }) }}</p>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="state.loading.value || !!state.preview.value?.submitting" @click="reload">
        <Icon name="refresh" size="sm" :class="state.loading.value ? 'animate-spin motion-reduce:animate-none' : ''" aria-hidden="true" />
        {{ t('admin.cacheSettings.reload') }}
      </button>
    </div>

    <div v-if="state.loading.value && !state.data.value" class="space-y-6" role="status" :aria-label="t('admin.cacheSettings.loading')" data-test="settings-loading">
      <div v-for="n in 3" :key="n" class="rounded-3xl bg-white p-6 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700">
        <div class="h-5 w-48 animate-pulse rounded bg-gray-100 motion-reduce:animate-none dark:bg-dark-700" />
        <div class="mt-3 h-4 w-96 max-w-full animate-pulse rounded bg-gray-100 motion-reduce:animate-none dark:bg-dark-700" />
        <div v-for="r in 2" :key="r" class="mt-6 flex justify-between gap-6">
          <div class="h-10 flex-1 animate-pulse rounded bg-gray-100 motion-reduce:animate-none dark:bg-dark-700" />
          <div class="h-10 w-28 animate-pulse rounded bg-gray-100 motion-reduce:animate-none dark:bg-dark-700" />
        </div>
      </div>
    </div>

    <div
      v-else-if="state.loadError.value && !state.data.value"
      role="alert"
      class="flex flex-wrap items-start gap-3 rounded-3xl p-5"
      :class="state.loadError.value === 'unavailable' ? 'bg-gray-50 text-gray-700 ring-1 ring-gray-900/5 dark:bg-dark-800 dark:text-dark-200 dark:ring-dark-700' : 'border border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200'"
      data-test="settings-load-error"
    >
      <Icon :name="state.loadError.value === 'unavailable' ? 'infoCircle' : 'exclamationCircle'" size="md" class="mt-0.5 shrink-0" aria-hidden="true" />
      <p class="min-w-0 flex-1 text-sm">
        {{ state.loadError.value === 'unavailable' ? t('admin.cacheSettings.unavailable') : t('admin.cacheSettings.loadFailed') }}
      </p>
      <button type="button" class="btn btn-secondary btn-sm" @click="reload">{{ t('admin.cacheSettings.retry') }}</button>
    </div>

    <template v-else-if="state.data.value">
      <MasterBar :state="state" :now="now" />
      <PolicyCard v-for="meta in cards" :key="meta.key" :meta="meta" :state="state" :now="now" />
      <ExistingMechanismsCard :existing="state.data.value.existing" />
    </template>

    <ConfirmPolicyDialog :state="state" />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { POLICY_CARDS } from './catalog'
import { usePolicies, type PolicyDeps } from './usePolicies'
import MasterBar from './components/MasterBar.vue'
import PolicyCard from './components/PolicyCard.vue'
import ExistingMechanismsCard from './components/ExistingMechanismsCard.vue'
import ConfirmPolicyDialog from './components/ConfirmPolicyDialog.vue'

const props = defineProps<{ deps?: Partial<PolicyDeps> }>()
const { t } = useI18n()
const state = usePolicies(props.deps)
const cards = POLICY_CARDS
const now = ref(props.deps?.now?.() ?? new Date())
let ticker: ReturnType<typeof setInterval> | null = null

function reload() {
  if (state.dirtyKeys.value.length && !window.confirm(t('admin.cacheSettings.leaveConfirm'))) return
  void state.load()
}

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!state.dirtyKeys.value.length) return
  event.preventDefault()
  event.returnValue = ''
}

onBeforeRouteLeave(() => (state.dirtyKeys.value.length ? window.confirm(t('admin.cacheSettings.leaveConfirm')) : true))

onMounted(() => {
  void state.load()
  window.addEventListener('beforeunload', onBeforeUnload)
  // Drives the "takes effect within N seconds" countdowns and relative times.
  ticker = setInterval(() => {
    now.value = props.deps?.now?.() ?? new Date()
  }, 1000)
})

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  if (ticker) clearInterval(ticker)
})

defineExpose({ state })
</script>
