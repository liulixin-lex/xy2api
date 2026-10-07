<template>
  <section class="card mt-6" data-testid="claude-cache-settings" aria-labelledby="claude-cache-title">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="claude-cache-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.claudeCacheFallback.title') }}</h2>
      <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.hint') }}</p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="loading" role="status" class="text-sm text-gray-600 dark:text-dark-300">{{ t('common.loading') }}</p>
      <div v-if="loadFailed" role="alert" class="flex flex-wrap items-center gap-3">
        <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.settings.claudeCacheFallback.loadFailed') }}</p>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ t('admin.settings.claudeCacheFallback.reload') }}</button>
      </div>
      <fieldset v-if="loaded" :disabled="saving || loading" class="min-w-0 space-y-5">
        <legend class="sr-only">{{ t('admin.settings.claudeCacheFallback.title') }}</legend>
        <div class="flex items-center justify-between gap-4">
          <span id="claude-cache-enabled" class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.settings.claudeCacheFallback.enabled') }}</span>
          <Toggle v-model="draft.enabled" aria-labelledby="claude-cache-enabled" />
        </div>
        <p class="text-sm text-amber-800 dark:text-amber-200">{{ t('admin.settings.claudeCacheFallback.cost') }}</p>
        <p v-if="!draft.rules.length" class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.empty') }}</p>
        <ClaudeCacheFallbackRuleEditor v-for="(rule, index) in draft.rules" :key="rowKeys[index]" :model-value="rule" :index="index" :groups="groups" @update:model-value="draft.rules[index] = $event" @remove="removeRule(index)" />
        <button type="button" class="btn btn-secondary" :disabled="draft.rules.length >= 100" @click="addRule">{{ t('admin.settings.claudeCacheFallback.add') }}</button>
      </fieldset>
      <p class="text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.propagation') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
      <p v-if="saved" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ t('admin.settings.claudeCacheFallback.saved') }}</p>
      <button v-if="loaded" type="button" class="btn btn-primary" :disabled="saving || loading || !dirty" @click="save">{{ saving ? t('common.saving') : t('admin.settings.claudeCacheFallback.save') }}</button>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type { ClaudeCacheFallbackPolicy } from '@/api/admin/settings'
import Toggle from '@/components/common/Toggle.vue'
import ClaudeCacheFallbackRuleEditor from './ClaudeCacheFallbackRuleEditor.vue'
const { t } = useI18n()
const draft = ref<ClaudeCacheFallbackPolicy>({ enabled: false, rules: [] })
const groups = ref<AdminGroup[]>([])
const loading = ref(false), saving = ref(false), loaded = ref(false), loadFailed = ref(false), saved = ref(false), error = ref('')
const baseline = ref(''), rowKeys = ref<number[]>([])
let sequence = 0, nextRow = 0, disposed = false
const dirty = computed(() => JSON.stringify(draft.value) !== baseline.value)
async function load() {
  if (loading.value) return
  loading.value = true; loadFailed.value = false; error.value = ''
  const current = ++sequence
  try {
    const [settings, allGroups] = await Promise.all([adminAPI.settings.getSettings(), adminAPI.groups.getAllIncludingInactive()])
    if (disposed || current !== sequence) return
    groups.value = allGroups.filter(g => g.platform === 'anthropic' || g.platform === 'composite')
    // Retrying a directory failure must not overwrite an edited draft.
    if (!loaded.value) {
      draft.value = settings.claude_cache_fallback_policy ?? { enabled: false, rules: [] }
      baseline.value = JSON.stringify(draft.value)
      rowKeys.value = draft.value.rules.map(() => ++nextRow)
      loaded.value = true
    }
  } catch { if (!disposed) loadFailed.value = true }
  finally { if (!disposed) loading.value = false }
}
function addRule() {
  if (draft.value.rules.length >= 100) return
  rowKeys.value.push(++nextRow)
  draft.value.rules.push({ id: `rule-${Date.now()}-${nextRow}`, group_id: 0, api_key_ids: [], account_id: 0, base_url: '', models: [] })
  saved.value = false
}
function removeRule(index: number) { draft.value.rules.splice(index, 1); rowKeys.value.splice(index, 1); saved.value = false }
async function save() {
  if (saving.value || !dirty.value) return
  error.value = ''; saved.value = false
  const policy: ClaudeCacheFallbackPolicy = JSON.parse(JSON.stringify(draft.value))
  policy.rules.forEach(r => { r.models = [...new Set(r.models.map(m => m.trim()).filter(Boolean))] })
  if (policy.rules.some(r => !/^[a-zA-Z0-9_-]{1,64}$/.test(r.id) || r.group_id <= 0 || r.account_id <= 0 || !r.base_url || !r.models.length || r.models.length > 50 || r.models.some(m => !m.trim() || m.includes('*')))) {
    error.value = t('admin.settings.claudeCacheFallback.invalid'); return
  }
  saving.value = true
  try {
    const result = await adminAPI.settings.updateSettings({ claude_cache_fallback_policy: policy })
    if (disposed) return
    draft.value = result.claude_cache_fallback_policy ?? policy
    baseline.value = JSON.stringify(draft.value); saved.value = true
  } catch { if (!disposed) error.value = t('admin.settings.claudeCacheFallback.saveFailed') }
  finally { if (!disposed) saving.value = false }
}
onMounted(load)
onBeforeUnmount(() => { disposed = true; sequence++ })
</script>
