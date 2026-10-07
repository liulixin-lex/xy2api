<template>
  <fieldset class="min-w-0 space-y-4 border-t border-gray-200 pt-5 dark:border-dark-600">
    <legend class="sr-only">{{ t('admin.settings.claudeCacheFallback.ruleLegend', { index: index + 1 }) }}</legend>
    <div class="flex flex-wrap items-end justify-between gap-3">
      <label class="block min-w-0 flex-1 text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.rule') }}
        <input :value="modelValue.id" maxlength="64" class="input mt-1 w-full" @input="patch({ id: input($event) })" />
      </label>
      <button type="button" class="btn btn-secondary" @click="$emit('remove')">{{ t('admin.settings.claudeCacheFallback.remove') }}</button>
    </div>
    <div class="grid min-w-0 gap-4 sm:grid-cols-2">
      <div class="min-w-0">
        <label :for="`cache-group-${index}`" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.group') }}</label>
        <Select :id="`cache-group-${index}`" :model-value="modelValue.group_id || null" :options="groupOptions" searchable :placeholder="t('admin.settings.claudeCacheFallback.select')" @update:model-value="changeGroup(Number($event))" />
      </div>
      <div class="min-w-0">
        <label :for="`cache-account-${index}`" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.account') }}</label>
        <Select :id="`cache-account-${index}`" :model-value="modelValue.account_id || null" :options="accountOptions" :disabled="!modelValue.group_id || loading" searchable :placeholder="t('admin.settings.claudeCacheFallback.select')" @update:model-value="selectAccount(Number($event))" />
      </div>
    </div>
    <p v-if="loading" role="status" class="text-sm text-gray-600 dark:text-dark-300">{{ t('common.loading') }}</p>
    <div v-if="failed" role="alert" class="flex flex-wrap items-center gap-3 text-sm text-red-700 dark:text-red-300">
      {{ t('admin.settings.claudeCacheFallback.loadFailed') }}
      <button type="button" class="btn btn-secondary" @click="loadOptions(true)">{{ t('admin.settings.claudeCacheFallback.reload') }}</button>
    </div>
    <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.url') }}
      <input :value="modelValue.base_url" readonly class="input mt-1 w-full font-mono text-xs" />
    </label>
    <p v-if="urlError" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ urlError }}</p>
    <div>
      <p class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.keys') }}</p>
      <p class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.keysHint') }}</p>
      <div class="mt-2 flex max-h-48 flex-wrap gap-x-5 gap-y-2 overflow-y-auto">
        <label v-for="key in keyOptions" :key="key.id" class="flex max-w-full items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
          <input type="checkbox" :checked="modelValue.api_key_ids.includes(key.id)" :disabled="!modelValue.api_key_ids.includes(key.id) && modelValue.api_key_ids.length >= 100" class="h-4 w-4 accent-primary-600" @change="toggleKey(key.id)" />
          <span class="truncate">{{ key.name }} #{{ key.id }}</span>
        </label>
      </div>
      <p class="mt-2 text-xs font-medium text-gray-700 dark:text-gray-200">{{ modelValue.api_key_ids.length ? t('admin.settings.claudeCacheFallback.selectedKeys', { count: modelValue.api_key_ids.length }) : t('admin.settings.claudeCacheFallback.allKeys') }}</p>
    </div>
    <button v-if="hasMore" type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="loadOptions(false)">{{ t('admin.settings.claudeCacheFallback.more') }}</button>
    <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.settings.claudeCacheFallback.models') }}
      <textarea :value="modelValue.models.join('\n')" rows="3" class="input mt-1 w-full font-mono text-sm" :aria-describedby="`cache-models-hint-${index}`" @input="patch({ models: input($event).split('\n') })" />
    </label>
    <p :id="`cache-models-hint-${index}`" class="text-xs text-gray-600 dark:text-dark-300">{{ t('admin.settings.claudeCacheFallback.modelsHint') }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type { ClaudeCacheFallbackRule } from '@/api/admin/settings'
import Select from '@/components/common/Select.vue'
const props = defineProps<{ modelValue: ClaudeCacheFallbackRule; index: number; groups: AdminGroup[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: ClaudeCacheFallbackRule]; remove: [] }>()
const { t } = useI18n()
const accounts = ref<{ id: number; name: string }[]>([]), keys = ref<{ id: number; name: string }[]>([])
const loading = ref(false), failed = ref(false), hasMore = ref(false), urlError = ref('')
let page = 0, sequence = 0, accountSequence = 0, disposed = false
const groupOptions = computed(() => props.groups.map(g => ({ value: g.id, label: `${g.name} #${g.id}` })))
const accountOptions = computed(() => {
  const options = accounts.value.map(a => ({ value: a.id, label: `${a.name} #${a.id}` }))
  if (props.modelValue.account_id && !options.some(a => a.value === props.modelValue.account_id)) options.unshift({ value: props.modelValue.account_id, label: t('admin.settings.claudeCacheFallback.accountID', { id: props.modelValue.account_id }) })
  return options
})
const keyOptions = computed(() => [...props.modelValue.api_key_ids.filter(id => !keys.value.some(k => k.id === id)).map(id => ({ id, name: t('admin.settings.claudeCacheFallback.keyID', { id }) })), ...keys.value])
function input(event: Event) { return (event.target as HTMLInputElement).value }
function patch(value: Partial<ClaudeCacheFallbackRule>) { emit('update:modelValue', { ...props.modelValue, ...value }) }
function changeGroup(id: number) { if (id !== props.modelValue.group_id) { accountSequence++; patch({ group_id: id, account_id: 0, base_url: '', api_key_ids: [], models: [] }) } }
function toggleKey(id: number) { patch({ api_key_ids: props.modelValue.api_key_ids.includes(id) ? props.modelValue.api_key_ids.filter(k => k !== id) : [...props.modelValue.api_key_ids, id] }) }
async function loadOptions(reset: boolean) {
  if (!props.modelValue.group_id) return
  const current = ++sequence, group = props.modelValue.group_id, nextPage = reset ? 1 : page + 1
  loading.value = true; failed.value = false
  try {
    const [a, k] = await Promise.all([adminAPI.accounts.list(nextPage, 50, { platform: 'anthropic', type: 'apikey', group: String(group) }), adminAPI.groups.getGroupApiKeys(group, nextPage, 50)])
    if (disposed || current !== sequence || group !== props.modelValue.group_id) return
    const newAccounts = a.items.filter(item => !item.extra?.anthropic_passthrough).map(item => ({ id: item.id, name: item.name }))
    const newKeys = k.items.map((item: { id: number; name: string }) => ({ id: item.id, name: item.name }))
    accounts.value = reset ? newAccounts : [...accounts.value, ...newAccounts]
    keys.value = reset ? newKeys : [...keys.value, ...newKeys]
    page = nextPage; hasMore.value = nextPage * 50 < Math.max(a.total, k.total)
  } catch { if (!disposed && current === sequence) failed.value = true }
  finally { if (!disposed && current === sequence) loading.value = false }
}
async function selectAccount(id: number) {
  const current = ++accountSequence, group = props.modelValue.group_id
  patch({ account_id: id, base_url: '', models: [] }); urlError.value = ''
  try {
    const account = await adminAPI.accounts.getById(id)
    if (disposed || current !== accountSequence || group !== props.modelValue.group_id) return
    const raw = String(account.credentials?.base_url || 'https://api.anthropic.com')
    const url = new URL(raw)
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || account.platform !== 'anthropic' || account.type !== 'apikey' || account.extra?.anthropic_passthrough) throw new Error('unsupported')
    patch({ account_id: id, base_url: raw.replace(/\/+$/, ''), models: [] })
  } catch { if (!disposed && current === accountSequence) urlError.value = t('admin.settings.claudeCacheFallback.urlMissing') }
}
watch(() => props.modelValue.group_id, () => { sequence++; accountSequence++; accounts.value = []; keys.value = []; page = 0; hasMore.value = false; urlError.value = ''; void loadOptions(true) }, { immediate: true })
onBeforeUnmount(() => { disposed = true; sequence++; accountSequence++ })
</script>
