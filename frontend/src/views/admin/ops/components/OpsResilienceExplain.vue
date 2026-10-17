<template>
  <div data-testid="ops-resilience-explain">
    <div class="mb-2">
      <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.resilience.explain.title') }}</h4>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.resilience.explain.opsHint') }}</p>
    </div>
    <form class="mb-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4" @submit.prevent="run">
      <label class="text-xs text-gray-700 dark:text-gray-300">{{ t('admin.resilience.explain.group') }}
        <Select :model-value="groupId" class="mt-1" :options="groupOptions" :placeholder="t('admin.resilience.explain.groupPlaceholder')" :aria-label="t('admin.resilience.explain.group')" data-testid="resilience-explain-group" @update:model-value="selectGroup" />
      </label>
      <label class="text-xs text-gray-700 dark:text-gray-300">{{ t('admin.resilience.explain.model') }}
        <input v-model.trim="model" type="text" class="input mt-1" autocomplete="off" data-testid="resilience-explain-model">
      </label>
      <label class="text-xs text-gray-700 dark:text-gray-300">{{ t('admin.resilience.explain.sessionHash') }}
        <input v-model.trim="session" type="text" class="input mt-1 font-mono" autocomplete="off" data-testid="resilience-explain-session">
      </label>
      <div class="flex items-end">
        <button type="submit" class="btn btn-secondary" :disabled="running || !groupId" data-testid="resilience-explain-run">{{ running ? t('admin.resilience.explain.running') : t('admin.resilience.explain.run') }}</button>
      </div>
    </form>
    <p v-if="error" role="alert" class="mb-2 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">{{ error }}</p>
    <ResilienceExplainResult v-if="result" :explanation="result" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import ResilienceExplainResult from '@/components/admin/resilience/ResilienceExplainResult.vue'
import { adminAPI } from '@/api/admin'
import type { ResilienceExplanation } from '@/api/admin/resilience'
import { extractApiErrorMessage } from '@/utils/apiError'

// groups, when given, comes from the parent panel; otherwise it is loaded
// here.
const props = defineProps<{ groups?: { id: number; name: string }[] }>()

const { t } = useI18n()

const loadedGroups = ref<{ id: number; name: string }[]>([])
const groups = computed(() => props.groups ?? loadedGroups.value)
const groupId = ref<number | null>(null)
const model = ref('')
const session = ref('')
const result = ref<ResilienceExplanation | null>(null)
const running = ref(false)
const error = ref('')
let request = 0

const groupOptions = computed(() => groups.value.map(g => ({ value: g.id, label: `${g.name} #${g.id}` })))

function selectGroup(value: string | number | boolean | null) {
  groupId.value = typeof value === 'number' ? value : null
}

watch(groupId, () => {
  request++
  result.value = null
  error.value = ''
  running.value = false
})

async function run() {
  if (running.value || !groupId.value) return
  error.value = ''
  const current = ++request
  running.value = true
  try {
    const query: { model?: string; session_hash?: string } = {}
    if (model.value) query.model = model.value
    if (session.value) query.session_hash = session.value
    const explanation = await adminAPI.resilience.explain(groupId.value, query)
    if (current === request) result.value = explanation
  } catch (cause) {
    if (current === request) error.value = extractApiErrorMessage(cause, t('admin.resilience.explain.failed'))
  } finally {
    if (current === request) running.value = false
  }
}

onMounted(async () => {
  if (props.groups) return
  try {
    const list = await adminAPI.groups.getAll()
    loadedGroups.value = list.map(g => ({ id: g.id, name: g.name }))
  } catch (cause) {
    error.value = extractApiErrorMessage(cause, t('admin.resilience.explain.groupsFailed'))
  }
})
</script>
