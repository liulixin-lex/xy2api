<template>
  <div class="space-y-4" data-testid="share-explain">
    <div class="flex flex-wrap items-center gap-3">
      <p class="min-w-0 flex-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.explain.hint') }}</p>
      <button type="button" class="btn btn-primary" :disabled="running" data-testid="share-explain-simulate" @click="run(true)">
        {{ running ? t('admin.shareScheduling.explain.running') : t('admin.shareScheduling.explain.simulate') }}
      </button>
    </div>

    <details class="rounded-xl border border-gray-200 p-3 dark:border-dark-600" data-testid="share-explain-more">
      <summary class="cursor-pointer text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.more') }}</summary>
      <form class="mt-3 space-y-4" @submit.prevent="run(false)">
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.model') }}
            <input v-model.trim="form.model" type="text" class="input mt-1" autocomplete="off" data-testid="share-explain-model">
          </label>
          <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.sessionHash') }}
            <input v-model.trim="form.session" type="text" class="input mt-1 font-mono" autocomplete="off" data-testid="share-explain-session">
            <span class="input-hint">{{ t('admin.shareScheduling.explain.sessionHashHint') }}</span>
          </label>
          <label class="text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.estimatedTokens') }}
            <input v-model.number="form.tokens" type="number" min="0" step="1" class="input mt-1" data-testid="share-explain-tokens">
          </label>
        </div>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
          <input v-model="form.oneHour" type="checkbox">{{ t('admin.shareScheduling.explain.cacheOneHour') }}
        </label>
        <label class="block text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.body') }}
          <textarea v-model="form.body" rows="4" class="input mt-1 font-mono text-xs" spellcheck="false" data-testid="share-explain-body" />
          <span class="input-hint">{{ t('admin.shareScheduling.explain.bodyHint') }}</span>
        </label>
        <button type="submit" class="btn btn-secondary" :disabled="running" data-testid="share-explain-run">{{ t('admin.shareScheduling.explain.run') }}</button>
      </form>
    </details>

    <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>

    <section v-if="result" class="space-y-4" data-testid="share-explain-result">
      <div class="rounded-xl bg-gray-50 p-4 text-sm text-gray-800 dark:bg-dark-700/60 dark:text-dark-100" data-testid="share-explain-summary">
        <p v-if="result.simulated_new_session" class="mb-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.summary.random') }}</p>
        <p v-if="result.bound_account_id">{{ t('admin.shareScheduling.explain.summary.bound', { account: accountName(result.bound_account_id) }) }}</p>
        <p v-if="first">{{ t('admin.shareScheduling.explain.summary.target', { account: accountName(first.account_id), priority: first.priority }) }}</p>
        <p v-else>{{ t('admin.shareScheduling.explain.summary.none') }}</p>
        <p v-if="fallback.length">{{ t('admin.shareScheduling.explain.summary.fallback', { list: fallback.map(accountName).join(' → ') }) }}</p>
      </div>

      <details class="rounded-xl border border-gray-200 p-3 dark:border-dark-600">
        <summary class="cursor-pointer text-sm text-gray-700 dark:text-dark-200">{{ t('admin.shareScheduling.explain.details') }}</summary>
        <div class="mt-3 space-y-4">
          <dl class="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <div v-for="item in summary" :key="item.key" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
              <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.result.' + item.key) }}</dt>
              <dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">{{ item.value }}</dd>
            </div>
          </dl>
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.explain.result.entries') }}</h4>
            <div class="mt-2 overflow-x-auto">
              <table class="w-full min-w-[760px] text-left text-xs">
                <thead>
                  <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-600 dark:text-dark-400">
                    <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.rank') }}</th>
                    <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.account') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.priority') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.weight') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.healthFactor') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.rampFactor') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.effective') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.loadRate') }}</th>
                    <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.state') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="entry in sortedEntries" :key="entry.account_id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                    <td class="p-2 tabular-nums">{{ entry.rank > 0 ? entry.rank : '-' }}</td>
                    <td class="p-2">{{ accountName(entry.account_id) }}</td>
                    <td class="p-2 text-right tabular-nums">{{ entry.priority }}</td>
                    <td class="p-2 text-right tabular-nums">{{ entry.load_factor }}</td>
                    <td class="p-2 text-right tabular-nums" :class="entry.health_factor !== null && entry.health_factor < 1 ? 'text-amber-700 dark:text-amber-300' : ''">×{{ formatShareFactor(entry.health_factor) }}</td>
                    <td class="p-2 text-right tabular-nums" :class="entry.ramp_factor < 1 ? 'text-sky-700 dark:text-sky-300' : ''">×{{ formatShareFactor(entry.ramp_factor) }}</td>
                    <td class="p-2 text-right font-medium tabular-nums">{{ formatShareWeight(entry.effective_weight) }}</td>
                    <td class="p-2 text-right tabular-nums">{{ entry.load_rate === null ? '—' : entry.load_rate + '%' }}</td>
                    <td class="p-2">
                      <span v-if="entry.excluded" class="text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.excluded') }}</span>
                      <span v-else-if="entry.saturated" class="text-amber-700 dark:text-amber-300">{{ t('admin.shareScheduling.explain.saturated') }}</span>
                      <span v-else-if="entry.eligible && !entry.reason" class="text-emerald-700 dark:text-emerald-300">{{ t('admin.shareScheduling.explain.eligible') }}</span>
                      <span v-else class="text-amber-700 dark:text-amber-300">{{ labels.reason(entry.reason) }}</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
          <div v-if="result.resilience" data-testid="share-explain-resilience">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.resilience.explain.title') }}</h4>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.resilience.explain.hint') }}</p>
            <ResilienceExplainResult class="mt-3" :explanation="result.resilience" />
          </div>
        </div>
      </details>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ResilienceExplainResult from '@/components/admin/resilience/ResilienceExplainResult.vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type { ShareExplainInput, ShareExplanation, ShareMember } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatShareFactor, formatShareWeight } from '@/utils/shareFactors'
import { formatShareMs } from '@/utils/shareScheduling'

const props = defineProps<{
  groupId: number
  members: ShareMember[]
}>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const form = reactive({ model: '', session: '', tokens: '' as number | '', oneHour: false, body: '' })
const result = ref<ShareExplanation | null>(null)
const running = ref(false)
const error = ref('')
let request = 0

const names = computed(() => new Map(props.members.map(m => [m.account_id, m.name])))
const accountName = (id: number) => `${names.value.get(id) ?? ''} #${id}`.trim()
const sortedEntries = computed(() => (result.value?.entries ?? []).slice().sort((a, b) => (a.rank || Infinity) - (b.rank || Infinity) || a.account_id - b.account_id))

/** The account the request would try first, and the ones after it. */
const first = computed(() => {
  const id = result.value?.steps[0]
  if (id === undefined) return null
  return result.value?.entries.find(entry => entry.account_id === id) ?? { account_id: id, priority: result.value?.active_priority ?? '-' }
})
const fallback = computed(() => (result.value?.steps ?? []).slice(1, 4))

const summary = computed(() => {
  const r = result.value
  if (!r) return []
  const none = t('admin.shareScheduling.explain.result.none')
  const cache = !r.cacheable ? t('admin.shareScheduling.explain.result.notCacheable') : r.warm ? t('admin.shareScheduling.explain.result.warm') : t('admin.shareScheduling.explain.result.cold')
  return [
    { key: 'reason', value: labels.reason(r.reason) },
    { key: 'keySource', value: labels.keySource(r.key_source) },
    { key: 'session', value: r.session || none },
    { key: 'bound', value: r.bound_account_id ? accountName(r.bound_account_id) : none },
    { key: 'idle', value: r.bound_account_id ? formatShareMs(r.idle_ms) : none },
    { key: 'cacheTTL', value: formatShareMs(r.cache_ttl_seconds * 1000) },
    { key: 'cache', value: `${cache} · ${r.estimated_tokens}` },
    { key: 'waitBudget', value: r.wait_budget_ms > 0 ? formatShareMs(r.wait_budget_ms) : none },
    { key: 'write', value: labels.write(r.write) },
    { key: 'spill', value: r.spill_reason ? `${labels.write(r.spill_write)} · ${labels.reason(r.spill_reason)}` : labels.write(r.spill_write) },
    { key: 'target', value: r.target ? accountName(r.target) : none },
    { key: 'ideal', value: r.ideal ? accountName(r.ideal) : none }
  ]
})

watch(() => props.groupId, () => {
  request++
  result.value = null
  error.value = ''
  running.value = false
})

/** Simulates a new session, or the request described by the form. */
async function run(newSession: boolean) {
  if (running.value) return
  error.value = ''
  // No session hash makes the server simulate a random new session.
  const input: ShareExplainInput = { model: newSession ? '' : form.model }
  if (!newSession) {
    if (form.session) input.session_hash = form.session
    input.cache_one_hour = form.oneHour
    if (typeof form.tokens === 'number' && form.tokens > 0) input.estimated_tokens = Math.floor(form.tokens)
    if (form.body.trim()) {
      try {
        input.body = JSON.parse(form.body)
      } catch {
        error.value = t('admin.shareScheduling.explain.bodyInvalid')
        return
      }
    }
  }
  const current = ++request
  running.value = true
  try {
    const explanation = await shareSchedulingAPI.explain(props.groupId, input)
    if (current === request) result.value = explanation
  } catch (err) {
    if (current === request) error.value = extractApiErrorMessage(err, t('admin.shareScheduling.explain.failed'))
  } finally {
    if (current === request) running.value = false
  }
}
</script>
