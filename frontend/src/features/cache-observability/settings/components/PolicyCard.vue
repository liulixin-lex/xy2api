<template>
  <section
    v-if="entry"
    :class="nested ? 'rounded-2xl bg-white p-4 ring-1 ring-gray-900/10 dark:bg-dark-800 dark:ring-dark-600 sm:p-5' : 'rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6'"
    :aria-labelledby="titleId"
    :data-test="`policy-${meta.key}`"
    :data-state="stateCode"
  >
    <header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      <div class="min-w-0">
        <component
          :is="nested ? 'h4' : 'h2'"
          :id="titleId"
          ref="heading"
          tabindex="-1"
          class="rounded text-base font-bold text-gray-900 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-white"
        >
          {{ t(`admin.cacheSettings.policies.${meta.key}.title`) }}
        </component>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t(`admin.cacheSettings.policies.${meta.key}.description`) }}</p>
        <p class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
          <!-- Risk is an outlined tag, state a filled pill, so the two never read alike (m-6, §11.7). -->
          <span
            class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-medium"
            :class="risk === 'low' ? 'border-gray-300 text-gray-700 dark:border-dark-500 dark:text-dark-200' : 'border-red-300 text-red-700 dark:border-red-700 dark:text-red-300'"
            data-test="policy-risk"
          >
            <Icon v-if="risk === 'highest'" name="exclamationTriangle" size="xs" aria-hidden="true" />
            {{ codeLabel('risk', risk, t, te) }}
          </span>
          <span class="font-mono text-gray-600 dark:text-dark-300">{{ meta.settingKey }}</span>
        </p>
      </div>
      <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
        <!-- A switched control is only a draft until it is previewed and confirmed; say so where the state is read. -->
        <span v-if="dirty" class="badge badge-warning" data-test="policy-unsaved">{{ t('admin.cacheSettings.unsaved', { n: changeCount }) }}</span>
        <span class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium" :class="tone.pill" data-test="policy-state">
          <span class="h-1.5 w-1.5 rounded-full" :class="tone.dot" aria-hidden="true" />
          {{ codeLabel('states', stateCode, t, te) }}
        </span>
      </div>
    </header>

    <p
      v-if="stale"
      class="mt-4 flex items-start gap-2 rounded-xl bg-sky-50 px-4 py-3 text-sm text-sky-900 dark:bg-sky-900/20 dark:text-sky-100"
      data-test="policy-stale"
    >
      <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
      {{ t('admin.cacheSettings.staleDraft', { actor: stale.stored.updated_by?.email || t('admin.cacheSettings.footer.unknownActor'), time: formatBoth(stale.stored.updated_at, t), rev: stale.stored.rev }) }}
    </p>

    <ul v-if="blockers.length" class="mt-3 space-y-1" role="list" data-test="policy-blockers">
      <li v-for="b in blockers" :key="b" class="flex items-start gap-2 text-sm text-red-700 dark:text-red-300">
        <Icon name="ban" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        {{ codeLabel('blockers', b, t, te) }}
      </li>
    </ul>
    <ul v-if="entry.effective.warnings.length" class="mt-3 space-y-1" role="list" data-test="policy-warnings">
      <li v-for="w in entry.effective.warnings" :key="w" class="flex flex-wrap items-start gap-x-2 gap-y-1 text-sm text-amber-800 dark:text-amber-300">
        <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        <span class="min-w-0 flex-1">{{ codeLabel('warnings', w, t, te) }}</span>
        <!-- Candidates left out of this answer come back with a new read (v1.0.3). -->
        <button
          v-if="CANDIDATE_WARNINGS.includes(w)"
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="state.loading.value || state.refreshing.value"
          data-test="reload-candidates"
          @click="reloadCandidates"
        >
          {{ t('admin.cacheSettings.candidates.reload') }}
        </button>
      </li>
    </ul>
    <PolicyReceipt :state="state" :policy-key="meta.key" :now="now" />

    <div class="mt-2 divide-y divide-gray-100 dark:divide-dark-700">
      <PolicyField
        v-for="field in entry.schema"
        :key="field.field"
        :policy-key="meta.key"
        :field="servedField(entry, field)"
        :value="getPath(draft, field.field)"
        :stored-value="getPath(base.stored.spec, field.field)"
        :default-value="getPath(entry.defaults, field.field)"
        :issues="issues.filter((i) => i.field === field.field)"
        :server-errors="serverErrors.filter((e) => e.field === field.field)"
        :lock="lockReason(entry, draft, field)"
        :disabled="busy"
        :clipped="entry.effective.clipped_fields.includes(field.field)"
        :groups="state.groups.value"
        :groups-failed="state.groupsFailed.value"
        :fallback-group-ids="state.fallbackGroupIds.value"
        @update="state.update(meta.key, field.field, $event)"
      />
    </div>

    <details v-if="meta.key === 'observability' && caps.length" class="group mt-2 border-t border-gray-100 pt-3 dark:border-dark-700" data-test="static-caps">
      <summary class="inline-flex cursor-pointer list-none items-center gap-1 rounded text-sm font-semibold text-gray-900 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-white [&::-webkit-details-marker]:hidden">
        <Icon name="chevronRight" size="xs" class="transition-transform group-open:rotate-90 motion-reduce:transition-none" aria-hidden="true" />
        {{ t('admin.cacheSettings.caps.title') }}
      </summary>
      <p class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.cacheSettings.caps.hint') }}</p>
      <dl class="mt-2 grid grid-cols-[max-content_max-content] gap-x-4 gap-y-1 text-sm">
        <template v-for="[name, value] in caps" :key="name">
          <dt class="font-mono text-xs leading-5 text-gray-600 dark:text-dark-300">{{ name }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ value }}</dd>
        </template>
      </dl>
    </details>

    <footer class="mt-4 flex flex-wrap items-center gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
      <dl class="flex min-w-0 flex-wrap gap-x-5 gap-y-1 text-xs text-gray-600 dark:text-dark-300" data-test="policy-provenance">
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
          <dt class="sr-only">{{ t('admin.cacheSettings.footer.updatedBy') }}</dt>
          <dd>{{ t('admin.cacheSettings.footer.neverChanged') }}</dd>
        </div>
        <div v-if="entry.stored.reason" class="flex min-w-0 gap-1">
          <dt>{{ t('admin.cacheSettings.footer.reason') }}</dt>
          <dd class="min-w-0 break-words text-gray-900 dark:text-gray-100">{{ entry.stored.reason }}</dd>
        </div>
        <div v-if="entry.loaded" class="flex gap-1">
          <dt>{{ t('admin.cacheSettings.footer.loaded') }}</dt>
          <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ t('admin.cacheSettings.footer.loadedValue', { rev: entry.loaded.rev, time: formatBoth(entry.loaded.loaded_at, t) }) }}</dd>
        </div>
      </dl>
      <div class="ml-auto flex flex-wrap items-center justify-end gap-2">
        <button
          v-if="dirty && errorFields.length"
          :id="blockedId"
          type="button"
          class="rounded text-xs font-medium text-red-700 underline underline-offset-2 hover:text-red-800 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-red-300"
          data-test="preview-blocked"
          @click="goToFirstError"
        >
          {{ t('admin.cacheSettings.fixFirst', { n: errorFields.length }) }}
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :aria-expanded="historyOpen" :aria-controls="historyId" @click="toggleHistory">
          <Icon name="clock" size="sm" aria-hidden="true" />
          {{ t('admin.cacheSettings.history.toggle') }}
        </button>
        <button v-if="dirty" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="state.discard(meta.key)">
          {{ t('admin.cacheSettings.discard') }}
        </button>
        <button
          ref="previewButton"
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="!dirty || busy || errorFields.length > 0"
          :aria-describedby="dirty && errorFields.length ? blockedId : undefined"
          :data-test="`preview-${meta.key}`"
          @click="state.openPreview(meta.key)"
        >
          {{ dirty ? t('admin.cacheSettings.previewCount', { n: changeCount }) : t('admin.cacheSettings.preview') }}
        </button>
      </div>
    </footer>
    <PolicyHistory v-if="historyOpen" :id="historyId" :policy-key="meta.key" :history="state.histories.value[meta.key]" @retry="state.loadHistory(meta.key)" />
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { controlIdFor, type PolicyCardMeta } from '../catalog'
import { returnFocusAfterPreview } from '../focusReturn'
import { codeLabel, formatBoth, formatUtc, relativeTime, stateTone } from '../format'
import { CANDIDATE_WARNINGS, getPath, lockReason, servedField } from '../schema'
import type { PoliciesState } from '../usePolicies'
import PolicyField from './PolicyField.vue'
import PolicyHistory from './PolicyHistory.vue'
import PolicyReceipt from './PolicyReceipt.vue'

const props = defineProps<{ meta: PolicyCardMeta; state: PoliciesState; now: Date; nested?: boolean }>()
const { t, te } = useI18n()

const titleId = computed(() => `cache-policy-${props.meta.key}`)
const historyId = computed(() => `cache-policy-history-${props.meta.key}`)
const blockedId = computed(() => `cache-policy-blocked-${props.meta.key}`)
const entry = computed(() => props.state.policy(props.meta.key))
const base = computed(() => props.state.baseOf(props.meta.key) ?? entry.value!)
const risk = computed(() => entry.value?.risk ?? 'low')
const draft = computed(() => props.state.draftOf(props.meta.key))
const changeCount = computed(() => props.state.changes(props.meta.key).length)
const dirty = computed(() => changeCount.value > 0)
const issues = computed(() => props.state.issues(props.meta.key))
const errorFields = computed(() => [...new Set(issues.value.filter((i) => i.level === 'error').map((i) => i.field))])
const busy = computed(() => !!props.state.preview.value?.submitting)
const stale = computed(() => props.state.staleBase(props.meta.key))
const serverErrors = computed(() => {
  const preview = props.state.preview.value
  if (!preview || preview.key !== props.meta.key) return []
  if (preview.failure?.kind === 'invalid') return preview.failure.errors
  return preview.result?.errors ?? []
})
const caps = computed(() => Object.entries(props.state.data.value?.static.caps ?? {}))
// The kill switch has its own page banner; repeating it on every card is noise.
const blockers = computed(() => (entry.value?.effective.blockers ?? []).filter((b) => b !== 'kill_switch'))

const stateCode = computed(() => props.state.stateOf(props.meta.key))
const tone = computed(() => stateTone(stateCode.value))

async function goToFirstError() {
  const field = errorFields.value[0]
  if (!field) return
  await nextTick()
  const row = document.querySelector<HTMLElement>(`[data-test="field-${props.meta.key}.${field}"]`)
  if (typeof row?.scrollIntoView === 'function') {
    const reduce = typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
    row.scrollIntoView({ block: 'center', behavior: reduce ? 'auto' : 'smooth' })
  }
  const control = document.getElementById(controlIdFor(props.meta.key, field))
  ;(control ?? row?.querySelector<HTMLElement>('input, select, button, [tabindex]'))?.focus({ preventScroll: true })
}

// Back to "Preview" after the confirmation closes; once the draft is saved that button is disabled, so the title.
const heading = ref<HTMLElement | null>(null)
const previewButton = ref<HTMLButtonElement | null>(null)
returnFocusAfterPreview(props.state, props.meta.key, () => [previewButton.value, heading.value])

// The warning and its button go once the candidates are back; keep focus in the card (m-new-1).
async function reloadCandidates() {
  await props.state.refresh()
  await nextTick()
  if (!document.activeElement || document.activeElement === document.body) heading.value?.focus()
}

const historyOpen = ref(false)
function toggleHistory() {
  historyOpen.value = !historyOpen.value
  if (historyOpen.value && !props.state.histories.value[props.meta.key]) void props.state.loadHistory(props.meta.key)
}
</script>
