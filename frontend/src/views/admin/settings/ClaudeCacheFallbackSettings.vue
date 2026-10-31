<template>
  <section class="card mt-6 p-6" data-testid="claude-cache-settings" aria-labelledby="claude-cache-title">
    <h2 id="claude-cache-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.claudeCacheFallback.title') }}</h2>
    <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.hint') }}</p>
    <div v-if="loading" class="mt-5 h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" role="status" :aria-label="t('common.loading')" />
    <div v-if="loadFailed || groupsFailed" class="mt-4 flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.settings.claudeCacheFallback.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="load">{{ t('admin.settings.claudeCacheFallback.reload') }}</button>
    </div>
    <div v-if="loaded" class="mt-5 space-y-5">
      <div class="space-y-3">
        <div class="flex items-start justify-between gap-4">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.claudeCacheFallback.enabled') }}</h3>
            <p class="mt-1 max-w-prose text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.enabledHint') }}</p>
          </div>
          <Toggle v-model="draft.enabled" :disabled="saving" :aria-label="t('admin.settings.claudeCacheFallback.enabled')" />
        </div>
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
      </div>
      <div class="flex flex-col gap-3 border-t border-gray-200 pt-5 dark:border-dark-600 sm:flex-row sm:items-start sm:justify-between sm:gap-4">
        <div>
          <h3 id="claude-cache-ttl-title" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.claudeCacheFallback.ttl') }}</h3>
          <p id="claude-cache-ttl-hint" class="mt-1 max-w-prose text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.ttlHint') }}</p>
        </div>
        <div class="flex shrink-0 gap-1" role="group" aria-labelledby="claude-cache-ttl-title" aria-describedby="claude-cache-ttl-hint">
          <button
            v-for="ttl in ttlOptions"
            :key="ttl"
            type="button"
            :data-ttl="ttl"
            class="rounded-md border px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 disabled:opacity-50"
            :class="draftTTL === ttl ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200' : 'border-gray-200 text-gray-700 dark:border-dark-600 dark:text-gray-200'"
            :aria-pressed="draftTTL === ttl"
            :disabled="saving"
            @click="selectTTL(ttl)"
          >{{ t(`admin.settings.claudeCacheFallback.ttl${ttl}`) }}</button>
        </div>
      </div>
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

// The duration row edits the administrator default that keys set to "follow global" use.
type CacheTTL = '5m' | '1h'
const ttlOptions: CacheTTL[] = ['5m', '1h']

const { t } = useI18n()
const draft = ref<ClaudeCacheFallbackPolicy>({ enabled: false, group_ids: [] })
const draftTTL = ref<CacheTTL>('5m')
const groups = ref<AdminGroup[]>([])
const loading = ref(false), saving = ref(false), loaded = ref(false)
const loadFailed = ref(false), groupsFailed = ref(false), saved = ref(false), error = ref('')
const baseline = ref(''), baselineTTL = ref<CacheTTL>('5m')
let disposed = false
const policyDirty = computed(() => JSON.stringify(draft.value) !== baseline.value)
const ttlDirty = computed(() => draftTTL.value !== baselineTTL.value)
const dirty = computed(() => policyDirty.value || ttlDirty.value)
const groupOptions = computed(() => groups.value.filter(g => !draft.value.group_ids.includes(g.id)).map(g => ({ value: g.id, label: g.name })))
const groupName = (id: number) => groups.value.find(g => g.id === id)?.name ?? t('admin.settings.claudeCacheFallback.groupID', { id })

function isPolicy(value: unknown): value is ClaudeCacheFallbackPolicy {
  if (!value || typeof value !== 'object') return false
  const p = value as Partial<ClaudeCacheFallbackPolicy>
  return typeof p.enabled === 'boolean' && Array.isArray(p.group_ids) && p.group_ids.every(id => Number.isSafeInteger(id) && id > 0)
}
const isTTL = (value: unknown): value is CacheTTL => value === '5m' || value === '1h'

async function load() {
  if (loading.value || saving.value) return
  loading.value = true; loadFailed.value = false; groupsFailed.value = false
  const [settings, directory] = await Promise.allSettled([adminAPI.settings.getSettings(), adminAPI.groups.getAllIncludingInactive()])
  if (disposed) return
  if (directory.status === 'fulfilled') groups.value = directory.value
  else groupsFailed.value = true
  if (settings.status === 'fulfilled' && isPolicy(settings.value.claude_cache_fallback_policy) && isTTL(settings.value.anthropic_cache_ttl_default)) {
    // Retrying the group directory must not discard an edited draft.
    if (!loaded.value) {
      draft.value = structuredClone(settings.value.claude_cache_fallback_policy)
      baseline.value = JSON.stringify(draft.value)
      draftTTL.value = baselineTTL.value = settings.value.anthropic_cache_ttl_default
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
function selectTTL(ttl: CacheTTL) { draftTTL.value = ttl; error.value = '' }

// Only changed settings are sent, so a duration change never re-validates the group scope.
async function save() {
  if (saving.value || loading.value || !dirty.value) return
  error.value = ''; saved.value = false
  const payload: { claude_cache_fallback_policy?: ClaudeCacheFallbackPolicy; anthropic_cache_ttl_default?: CacheTTL } = {}
  const policy = policyDirty.value ? { enabled: draft.value.enabled, group_ids: [...draft.value.group_ids] } : undefined
  if (policy) {
    if (policy.enabled && !policy.group_ids.length) { error.value = t('admin.settings.claudeCacheFallback.invalid'); return }
    payload.claude_cache_fallback_policy = policy
  }
  const ttl = ttlDirty.value ? draftTTL.value : undefined
  if (ttl) payload.anthropic_cache_ttl_default = ttl
  saving.value = true
  try {
    const result = await adminAPI.settings.updateSettings(payload)
    if (disposed) return
    const returned = result.claude_cache_fallback_policy
    if (policy && (!isPolicy(returned) || returned.enabled !== policy.enabled || JSON.stringify([...returned.group_ids].sort((a,b) => a-b)) !== JSON.stringify(policy.group_ids))) throw new Error('Cache settings were not saved')
    if (ttl && result.anthropic_cache_ttl_default !== ttl) throw new Error('Cache TTL was not saved')
    if (policy && isPolicy(returned)) { draft.value = returned; baseline.value = JSON.stringify(draft.value) }
    if (ttl) baselineTTL.value = ttl
    saved.value = true
  } catch { if (!disposed) error.value = t('admin.settings.claudeCacheFallback.saveFailed') }
  finally { if (!disposed) saving.value = false }
}
onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>
