<template>
  <div class="space-y-5" data-testid="share-explain">
    <form class="card space-y-4 p-5" @submit.prevent="run">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.explain.hint') }}</p>
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
        <textarea v-model="form.body" rows="5" class="input mt-1 font-mono text-xs" spellcheck="false" data-testid="share-explain-body" />
        <span class="input-hint">{{ t('admin.shareScheduling.explain.bodyHint') }}</span>
      </label>
      <div class="flex flex-wrap items-center gap-3">
        <button type="submit" class="btn btn-primary" :disabled="running" data-testid="share-explain-run">{{ running ? t('admin.shareScheduling.explain.running') : t('admin.shareScheduling.explain.run') }}</button>
        <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
      </div>
    </form>

    <section v-if="result" class="card space-y-5 p-5" data-testid="share-explain-result">
      <dl class="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="item in summary" :key="item.key" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-700/60">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.result.' + item.key) }}</dt>
          <dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">{{ item.value }}</dd>
        </div>
      </dl>
      <div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.explain.result.steps') }}</h4>
        <ol v-if="result.steps.length" class="mt-2 flex flex-wrap gap-2 text-sm" data-testid="share-explain-steps">
          <li v-for="(id, index) in result.steps" :key="id" class="rounded-lg bg-primary-50 px-2 py-1 text-primary-800 dark:bg-primary-900/20 dark:text-primary-200">{{ index + 1 }}. {{ accountName(id) }}</li>
        </ol>
        <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.result.noSteps') }}</p>
      </div>
      <div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.explain.result.entries') }}</h4>
        <div class="mt-2 overflow-x-auto">
          <table class="w-full min-w-[720px] text-left text-xs">
            <thead>
              <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.rank') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.account') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.tier') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.weight') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.explain.entryColumns.effectiveWeight') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.domain') }}</th>
                <th class="p-2">{{ t('admin.shareScheduling.explain.entryColumns.state') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in sortedEntries" :key="entry.account_id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                <td class="p-2 font-mono">{{ entry.rank > 0 ? entry.rank : '-' }}</td>
                <td class="p-2">{{ accountName(entry.account_id) }}</td>
                <td class="p-2 text-right font-mono">{{ entry.tier }}</td>
                <td class="p-2 text-right font-mono">{{ entry.weight }}</td>
                <td class="p-2 text-right font-mono">{{ entry.effective_weight.toFixed(2) }}</td>
                <td class="p-2 font-mono">{{ entry.cache_domain }}</td>
                <td class="p-2">
                  <span v-if="entry.excluded" class="text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.explain.excluded') }}</span>
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

const summary = computed(() => {
  const r = result.value
  if (!r) return []
  const none = t('admin.shareScheduling.explain.result.none')
  const cache = !r.cacheable ? t('admin.shareScheduling.explain.result.notCacheable') : r.warm ? t('admin.shareScheduling.explain.result.warm') : t('admin.shareScheduling.explain.result.cold')
  const items = [
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
  return items
})

watch(() => props.groupId, () => {
  request++
  result.value = null
  error.value = ''
  running.value = false
})

async function run() {
  if (running.value) return
  error.value = ''
  const input: ShareExplainInput = { model: form.model, session_hash: form.session, cache_one_hour: form.oneHour }
  if (typeof form.tokens === 'number' && form.tokens > 0) input.estimated_tokens = Math.floor(form.tokens)
  if (form.body.trim()) {
    try {
      input.body = JSON.parse(form.body)
    } catch {
      error.value = t('admin.shareScheduling.explain.bodyInvalid')
      return
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
