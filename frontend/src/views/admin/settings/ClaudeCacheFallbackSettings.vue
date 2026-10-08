<template>
  <section class="card mt-6 p-6" data-testid="claude-cache-settings" aria-labelledby="claude-cache-title">
    <div class="flex items-start justify-between gap-4">
      <div>
        <h2 id="claude-cache-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.claudeCacheFallback.title') }}</h2>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.hint') }}</p>
      </div>
      <Toggle v-if="loaded" v-model="draft.enabled" :disabled="saving" :aria-label="t('admin.settings.claudeCacheFallback.enabled')" />
    </div>
    <div v-if="loading" class="mt-5 h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" role="status" :aria-label="t('common.loading')" />
    <div v-if="loadFailed || groupsFailed" class="mt-4 flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.settings.claudeCacheFallback.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="load">{{ t('admin.settings.claudeCacheFallback.reload') }}</button>
    </div>
    <div v-if="loaded" class="mt-5 space-y-4">
      <div class="max-w-xl">
        <label for="claude-cache-groups" class="input-label">{{ t('admin.settings.claudeCacheFallback.groups') }}</label>
        <Select
          id="claude-cache-groups"
          :model-value="null"
          :options="groupOptions"
          :disabled="saving || loading || groupsFailed"
          :aria-label="t('admin.settings.claudeCacheFallback.groups')"
          :placeholder="t('admin.settings.claudeCacheFallback.selectGroups')"
          :empty-text="t('admin.settings.claudeCacheFallback.noGroups')"
          searchable
          @update:model-value="addGroup"
        />
        <ul v-if="draft.group_ids.length" class="mt-2 flex flex-wrap gap-2" :aria-label="t('admin.settings.claudeCacheFallback.groups')">
          <li v-for="id in draft.group_ids" :key="id" class="flex min-w-0 max-w-full items-center gap-1 rounded-md bg-gray-100 py-1 pl-2.5 pr-1 text-sm text-gray-800 dark:bg-dark-700 dark:text-gray-100">
            <span class="min-w-0 break-words">{{ groupName(id) }}</span>
            <button type="button" class="shrink-0 rounded p-1 text-gray-600 hover:bg-gray-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:text-dark-300 dark:hover:bg-dark-600" :disabled="saving" :aria-label="t('admin.settings.claudeCacheFallback.removeGroup', { name: groupName(id) })" @click="removeGroup(id)">
              <Icon name="x" size="sm" />
            </button>
          </li>
        </ul>
      </div>
      <p class="max-w-prose text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.cost') }}</p>
      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="btn btn-primary" :disabled="saving || loading || !dirty" @click="save">{{ saving ? t('common.saving') : t('admin.settings.claudeCacheFallback.save') }}</button>
        <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
        <p v-else-if="saved && !dirty" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ t('admin.settings.claudeCacheFallback.saved') }}</p>
      </div>
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
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const draft = ref<ClaudeCacheFallbackPolicy>({ enabled: false, group_ids: [] })
const groups = ref<AdminGroup[]>([])
const loading = ref(false), saving = ref(false), loaded = ref(false)
const loadFailed = ref(false), groupsFailed = ref(false), saved = ref(false), error = ref('')
const baseline = ref('')
let disposed = false
const dirty = computed(() => JSON.stringify(draft.value) !== baseline.value)
const groupOptions = computed(() => groups.value.filter(g => !draft.value.group_ids.includes(g.id)).map(g => ({ value: g.id, label: g.name })))
const groupName = (id: number) => groups.value.find(g => g.id === id)?.name ?? t('admin.settings.claudeCacheFallback.groupID', { id })

function isPolicy(value: unknown): value is ClaudeCacheFallbackPolicy {
  if (!value || typeof value !== 'object') return false
  const p = value as Partial<ClaudeCacheFallbackPolicy>
  return typeof p.enabled === 'boolean' && Array.isArray(p.group_ids) && p.group_ids.every(id => Number.isSafeInteger(id) && id > 0)
}

async function load() {
  if (loading.value || saving.value) return
  loading.value = true; loadFailed.value = false; groupsFailed.value = false
  const [settings, directory] = await Promise.allSettled([adminAPI.settings.getSettings(), adminAPI.groups.getAllIncludingInactive()])
  if (disposed) return
  if (directory.status === 'fulfilled') groups.value = directory.value
  else groupsFailed.value = true
  if (settings.status === 'fulfilled' && isPolicy(settings.value.claude_cache_fallback_policy)) {
    // Retrying the group directory must not discard an edited draft.
    if (!loaded.value) {
      draft.value = structuredClone(settings.value.claude_cache_fallback_policy)
      baseline.value = JSON.stringify(draft.value)
      loaded.value = true
    }
  } else loadFailed.value = true
  loading.value = false
}

function addGroup(value: string | number | boolean | null) {
  if (typeof value !== 'number' || draft.value.group_ids.includes(value)) return
  draft.value.group_ids = [...draft.value.group_ids, value].sort((a, b) => a - b)
  error.value = ''
}
function removeGroup(id: number) { draft.value.group_ids = draft.value.group_ids.filter(value => value !== id); error.value = '' }

async function save() {
  if (saving.value || loading.value || !dirty.value) return
  error.value = ''; saved.value = false
  const policy = { enabled: draft.value.enabled, group_ids: [...draft.value.group_ids] }
  if (policy.enabled && !policy.group_ids.length) { error.value = t('admin.settings.claudeCacheFallback.invalid'); return }
  saving.value = true
  try {
    const result = await adminAPI.settings.updateSettings({ claude_cache_fallback_policy: policy })
    if (disposed) return
    const returned = result.claude_cache_fallback_policy
    if (!isPolicy(returned) || returned.enabled !== policy.enabled || JSON.stringify([...returned.group_ids].sort((a,b) => a-b)) !== JSON.stringify(policy.group_ids)) throw new Error('Cache settings were not saved')
    draft.value = returned
    baseline.value = JSON.stringify(draft.value); saved.value = true
  } catch { if (!disposed) error.value = t('admin.settings.claudeCacheFallback.saveFailed') }
  finally { if (!disposed) saving.value = false }
}
onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>
