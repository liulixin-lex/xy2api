<template>
  <section
    class="rounded-3xl p-5 shadow-sm ring-1 sm:p-6"
    :class="available ? 'bg-white ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700' : 'bg-gray-50 ring-gray-900/5 dark:bg-dark-900/60 dark:ring-dark-700'"
    :aria-labelledby="titleId"
    :data-test="`policy-${meta.key}`"
    :data-state="entry?.effective.state"
  >
    <header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      <div class="min-w-0">
        <h2 :id="titleId" class="flex flex-wrap items-center gap-2 text-base font-bold" :class="available ? 'text-gray-900 dark:text-white' : 'text-gray-600 dark:text-dark-300'">
          {{ t(`admin.cacheSettings.policies.${meta.key}.title`) }}
          <span class="badge" :class="riskBadge.cls">
            <Icon v-if="riskBadge.icon" name="exclamationTriangle" size="xs" aria-hidden="true" />
            {{ codeLabel('risk', risk, t, te) }}
          </span>
        </h2>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t(`admin.cacheSettings.policies.${meta.key}.description`) }}</p>
        <p class="mt-1 font-mono text-xs text-gray-500 dark:text-dark-400">{{ meta.settingKey }}</p>
      </div>
      <span class="badge shrink-0" :class="stateBadge" data-test="policy-state">
        {{ stateText }}
      </span>
    </header>

    <template v-if="available && entry">
      <p
        v-if="gated && !state.masterOn.value"
        class="mt-4 flex items-start gap-2 rounded-xl bg-gray-100 px-4 py-3 text-sm text-gray-800 dark:bg-dark-700/60 dark:text-dark-100"
        data-test="master-note"
      >
        <Icon name="lock" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        {{ entry.effective.state === 'masked_by_master' ? t('admin.cacheSettings.maskedCard') : t('admin.cacheSettings.gatedCard') }}
      </p>

      <ul v-if="entry.effective.blockers.length" class="mt-3 space-y-1" role="list" data-test="policy-blockers">
        <li v-for="b in entry.effective.blockers" :key="b" class="flex items-start gap-2 text-sm text-red-700 dark:text-red-300">
          <Icon name="ban" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
          {{ codeLabel('blockers', b, t, te) }}
        </li>
      </ul>
      <ul v-if="entry.effective.warnings.length" class="mt-3 space-y-1" role="list" data-test="policy-warnings">
        <li v-for="w in entry.effective.warnings" :key="w" class="flex items-start gap-2 text-sm text-amber-800 dark:text-amber-300">
          <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
          {{ codeLabel('warnings', w, t, te) }}
        </li>
      </ul>
      <p v-if="pendingText" class="mt-3 flex items-start gap-2 text-sm text-emerald-700 dark:text-emerald-300" role="status" data-test="policy-pending">
        <Icon name="clock" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        {{ pendingText }}
      </p>

      <div class="mt-2 divide-y divide-gray-100 dark:divide-dark-700">
        <PolicyField
          v-for="field in entry.schema"
          :key="field.field"
          :policy-key="meta.key"
          :field="field"
          :value="getPath(draft, field.field)"
          :stored-value="getPath(entry.stored.spec, field.field)"
          :default-value="getPath(entry.defaults, field.field)"
          :issues="issues.filter((i) => i.field === field.field)"
          :server-errors="serverErrors.filter((e) => e.field === field.field)"
          :lock="lockReason(entry, draft, field)"
          :disabled="readOnly"
          :masked="gated && !state.masterOn.value && isConfigured(entry, draft, field)"
          :clipped="entry.effective.clipped_fields.includes(field.field)"
          :groups="state.groups.value"
          :groups-failed="state.groupsFailed.value"
          :fallback-group-ids="state.fallbackGroupIds.value"
          @update="state.update(meta.key, field.field, $event)"
        />
      </div>

      <div v-if="meta.key === 'observability' && caps.length" class="mt-2 border-t border-gray-100 pt-4 dark:border-dark-700">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.cacheSettings.caps.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.caps.hint') }}</p>
        <dl class="mt-2 grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
          <div v-for="[name, value] in caps" :key="name" class="flex justify-between gap-4">
            <dt class="break-all font-mono text-xs text-gray-600 dark:text-dark-300">{{ name }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ value }}</dd>
          </div>
        </dl>
      </div>

      <footer class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
        <dl class="flex flex-wrap gap-x-5 gap-y-1 text-xs text-gray-600 dark:text-dark-300" data-test="policy-provenance">
          <div class="flex gap-1">
            <dt>{{ t('admin.cacheSettings.footer.rev') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ entry.stored.rev }}</dd>
          </div>
          <div v-if="entry.stored.updated_at" class="flex gap-1">
            <dt>{{ t('admin.cacheSettings.footer.updatedBy') }}</dt>
            <dd class="text-gray-900 dark:text-gray-100">
              {{ entry.stored.updated_by?.email || t('admin.cacheSettings.footer.unknownActor') }},
              <time :datetime="entry.stored.updated_at" :title="formatUtc(entry.stored.updated_at)">{{ relativeTime(entry.stored.updated_at, now, t) }}</time>
            </dd>
          </div>
          <div v-else class="flex gap-1">
            <dd>{{ t('admin.cacheSettings.footer.neverChanged') }}</dd>
          </div>
          <div v-if="entry.stored.reason" class="flex min-w-0 gap-1">
            <dt>{{ t('admin.cacheSettings.footer.reason') }}</dt>
            <dd class="min-w-0 break-words text-gray-900 dark:text-gray-100">{{ entry.stored.reason }}</dd>
          </div>
          <div v-if="entry.loaded" class="flex gap-1">
            <dt>{{ t('admin.cacheSettings.footer.loaded') }}</dt>
            <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ t('admin.cacheSettings.footer.loadedValue', { rev: entry.loaded.rev, time: formatUtc(entry.loaded.loaded_at) }) }}</dd>
          </div>
        </dl>
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :aria-expanded="historyOpen"
            :aria-controls="historyId"
            @click="toggleHistory"
          >
            <Icon name="clock" size="sm" aria-hidden="true" />
            {{ t('admin.cacheSettings.history.toggle') }}
          </button>
          <button v-if="dirty" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="state.discard(meta.key)">
            {{ t('admin.cacheSettings.discard') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="!dirty || busy || readOnly || hasErrors"
            :data-test="`preview-${meta.key}`"
            @click="state.openPreview(meta.key)"
          >
            {{ dirty ? t('admin.cacheSettings.previewCount', { n: changeCount }) : t('admin.cacheSettings.preview') }}
          </button>
        </div>
      </footer>
      <PolicyHistory v-if="historyOpen" :id="historyId" :history="state.histories.value[meta.key]" :now="now" @retry="state.loadHistory(meta.key)" />
    </template>

    <p v-else class="mt-4 text-sm text-gray-600 dark:text-dark-300" data-test="policy-not-in-build">
      {{ t('admin.cacheSettings.notInBuild', { milestone: meta.milestone }) }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { PolicyCardMeta } from '../catalog'
import { codeLabel, formatUtc, formatUtcClock, relativeTime } from '../format'
import { getPath, isConfigured, lockReason } from '../schema'
import type { PoliciesState } from '../usePolicies'
import PolicyField from './PolicyField.vue'
import PolicyHistory from './PolicyHistory.vue'

const props = defineProps<{ meta: PolicyCardMeta; state: PoliciesState; now: Date }>()
const { t, te } = useI18n()

const titleId = computed(() => `cache-policy-${props.meta.key}`)
const historyId = computed(() => `cache-policy-history-${props.meta.key}`)
const entry = computed(() => props.state.entry(props.meta.key))
const available = computed(() => !!entry.value?.available)
const gated = computed(() => entry.value?.gated_by_master ?? props.meta.gated)
const risk = computed(() => entry.value?.risk ?? (props.meta.gated ? 'high' : 'low'))
const draft = computed(() => props.state.draftOf(props.meta.key))
const changeCount = computed(() => props.state.changes(props.meta.key).length)
const dirty = computed(() => changeCount.value > 0)
const issues = computed(() => props.state.issues(props.meta.key))
const hasErrors = computed(() => issues.value.some((i) => i.level === 'error'))
const busy = computed(() => !!props.state.preview.value?.submitting)
const readOnly = computed(() => props.state.killSwitch.value || busy.value)
const serverErrors = computed(() => {
  const preview = props.state.preview.value
  if (!preview || preview.key !== props.meta.key) return []
  if (preview.failure?.kind === 'invalid') return preview.failure.errors
  return preview.result?.errors ?? []
})
const caps = computed(() => Object.entries(props.state.data.value?.static.caps ?? {}))

const riskBadge = computed(() => {
  if (risk.value === 'highest') return { cls: 'badge-danger', icon: true }
  if (risk.value === 'high') return { cls: 'badge-danger', icon: false }
  return { cls: 'badge-gray', icon: false }
})

const stateCode = computed(() => (available.value ? entry.value?.effective.state ?? 'off' : 'not_in_build'))
const stateText = computed(() =>
  stateCode.value === 'not_in_build'
    ? t('admin.cacheSettings.notInBuildShort', { milestone: props.meta.milestone })
    : codeLabel('states', stateCode.value, t, te),
)
const stateBadge = computed(() => {
  switch (stateCode.value) {
    case 'on':
      return 'badge-success'
    case 'pending_reload':
      return 'badge-primary'
    case 'killed':
    case 'parse_error':
    case 'blocked':
      return 'badge-danger'
    case 'masked_by_master':
      return 'badge-warning'
    default:
      return 'badge-gray'
  }
})

const pendingText = computed(() => {
  const receipt = props.state.receipts.value[props.meta.key]
  if (receipt && props.now < receipt.effectiveBy) {
    return t('admin.cacheSettings.written', { rev: receipt.rev, time: formatUtcClock(receipt.effectiveBy) })
  }
  const e = entry.value
  if (e?.effective.state === 'pending_reload' && e.stored.updated_at) {
    const by = new Date(new Date(e.stored.updated_at).getTime() + props.state.refreshSeconds.value * 1000)
    const seconds = Math.max(0, Math.ceil((by.getTime() - props.now.getTime()) / 1000))
    return t('admin.cacheSettings.pendingReload', { stored: e.stored.rev, loaded: e.loaded?.rev ?? '-', seconds })
  }
  return ''
})

const historyOpen = ref(false)
function toggleHistory() {
  historyOpen.value = !historyOpen.value
  if (historyOpen.value && !props.state.histories.value[props.meta.key]) void props.state.loadHistory(props.meta.key)
}
</script>
