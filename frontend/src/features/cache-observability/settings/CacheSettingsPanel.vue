<template>
  <div class="space-y-6" data-test="cache-settings-panel">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <p class="max-w-3xl text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.intro', { seconds: state.refreshSeconds.value }) }}</p>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="state.loading.value || !!state.preview.value?.submitting" @click="reload">
        <Icon name="refresh" size="sm" :class="state.loading.value || state.refreshing.value ? 'animate-spin motion-reduce:animate-none' : ''" aria-hidden="true" />
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

      <dl class="flex flex-wrap gap-x-6 gap-y-1 px-1 text-xs text-gray-600 dark:text-dark-300" data-test="instance-status">
        <div v-if="state.data.value.process_started_at" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.status.processStarted') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatUtc(state.data.value.process_started_at) }}</dd>
        </div>
        <div v-if="state.data.value.build_version" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.status.build') }}</dt>
          <dd class="text-gray-900 dark:text-gray-100">{{ state.data.value.build_version }}</dd>
        </div>
        <div class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.status.refresh') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ t('admin.cacheSettings.status.refreshValue', { seconds: state.refreshSeconds.value }) }}</dd>
        </div>
        <div class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.status.readAt') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatUtc(state.data.value.generated_at) }}</dd>
        </div>
      </dl>

      <!-- Passive keys first, then the master group with the keys it gates (§11.7 page structure). -->
      <PolicyCard v-for="meta in passiveCards" :key="meta.key" :meta="meta" :state="state" :now="now" />
      <p v-if="passiveMissing.length" class="flex items-start gap-2 px-1 text-sm text-gray-600 dark:text-dark-300" data-test="not-included">
        <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        {{ t('admin.cacheSettings.notIncluded', { names: passiveMissing.join(t('admin.cacheSettings.nameSeparator')) }) }}
      </p>
      <MasterBar :state="state" :now="now" :missing="gatedMissing">
        <PolicyCard v-for="meta in gatedCards" :key="meta.key" nested :meta="meta" :state="state" :now="now" />
      </MasterBar>
      <ExistingMechanismsCard :existing="state.data.value.existing" />
    </template>

    <ConfirmPolicyDialog :state="state" />
    <ConfirmDialog
      :show="!!pendingDiscard"
      :title="t('admin.cacheSettings.leave.title')"
      :message="t('admin.cacheSettings.leave.message', { n: state.dirtyKeys.value.length })"
      :confirm-text="t('admin.cacheSettings.leave.discard')"
      :cancel-text="t('admin.cacheSettings.leave.stay')"
      danger
      @confirm="answerDiscard(true)"
      @cancel="answerDiscard(false)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { POLICY_CARDS, type PolicyCardMeta } from './catalog'
import { formatUtc } from './format'
import { usePolicies, type PolicyDeps } from './usePolicies'
import MasterBar from './components/MasterBar.vue'
import PolicyCard from './components/PolicyCard.vue'
import ExistingMechanismsCard from './components/ExistingMechanismsCard.vue'
import ConfirmPolicyDialog from './components/ConfirmPolicyDialog.vue'

const props = defineProps<{ deps?: Partial<PolicyDeps> }>()
const { t } = useI18n()
const state = usePolicies(props.deps)
const clock = () => props.deps?.now?.() ?? new Date()
const now = ref(clock())

const isGated = (meta: PolicyCardMeta) => state.entry(meta.key)?.gated_by_master ?? meta.gated
const isShipped = (meta: PolicyCardMeta) => !!state.policy(meta.key)
const title = (meta: PolicyCardMeta) => t(`admin.cacheSettings.policies.${meta.key}.title`)
const passiveCards = computed(() => POLICY_CARDS.filter((m) => isShipped(m) && !isGated(m)))
const gatedCards = computed(() => POLICY_CARDS.filter((m) => isShipped(m) && isGated(m)))
// Keys this build does not ship collapse into one line per group: no card, no risk tag (m-2).
const passiveMissing = computed(() => POLICY_CARDS.filter((m) => !isShipped(m) && !isGated(m)).map(title))
const gatedMissing = computed(() => POLICY_CARDS.filter((m) => !isShipped(m) && isGated(m)).map(title))

// In-app confirmation instead of window.confirm (n-1); the browser's own prompt still guards a tab close.
const pendingDiscard = shallowRef<((ok: boolean) => void) | null>(null)
function confirmDiscard(): Promise<boolean> {
  if (!state.dirtyKeys.value.length) return Promise.resolve(true)
  pendingDiscard.value?.(false)
  return new Promise((resolve) => {
    pendingDiscard.value = resolve
  })
}
function answerDiscard(ok: boolean) {
  const resolve = pendingDiscard.value
  pendingDiscard.value = null
  resolve?.(ok)
}

async function reload() {
  if (await confirmDiscard()) void state.load()
}

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!state.dirtyKeys.value.length) return
  event.preventDefault()
  event.returnValue = ''
}

onBeforeRouteLeave(() => confirmDiscard())

// The clock ticks every second only while a countdown runs; otherwise every 30 s for relative times (n-4).
let timer: ReturnType<typeof setTimeout> | null = null
function schedule(delay = state.hasCountdown() ? 1000 : 30_000) {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => {
    now.value = clock()
    state.tick(now.value)
    schedule()
  }, delay)
}
watch(
  () => state.hasCountdown(),
  (running) => {
    if (!running || !timer) return
    now.value = clock()
    schedule(1000)
  },
)

onMounted(() => {
  void state.load()
  window.addEventListener('beforeunload', onBeforeUnload)
  schedule()
})

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  if (timer) clearTimeout(timer)
  timer = null
  answerDiscard(false)
})

defineExpose({ state })
</script>
