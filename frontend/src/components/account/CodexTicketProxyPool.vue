<template>
  <section class="space-y-3" aria-labelledby="state-proxy-pool-title" data-testid="state-proxy-pool">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h3 id="state-proxy-pool-title" class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.stateProxies.title') }}</h3>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving || !loaded || entries.length >= 20" @click="add">{{ t('admin.settings.stateProxies.add') }}</button>
    </div>
    <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.stateProxies.hint') }}</p>
    <p v-if="loading" role="status" class="text-sm">{{ t('common.loading') }}</p>
    <p v-else-if="loaded && !entries.length" class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.stateProxies.empty') }}</p>
    <div class="divide-y divide-gray-200 dark:divide-dark-600">
      <div v-for="(entry, index) in entries" :key="entry.id" class="space-y-3 py-4" data-testid="state-proxy-row">
        <div class="grid items-end gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]">
          <div class="min-w-0">
            <label :for="`state-proxy-name-${entry.id}`" class="input-label">{{ t('admin.settings.stateProxies.name', { index: index + 1 }) }}</label>
            <input :id="`state-proxy-name-${entry.id}`" v-model="entry.name" maxlength="80" class="input w-full" :disabled="saving" @input="markDirty" />
          </div>
          <div class="min-w-0">
            <label :for="`state-proxy-url-${entry.id}`" class="input-label">{{ t('admin.settings.stateProxies.address') }}</label>
            <input :id="`state-proxy-url-${entry.id}`" v-model="entry.url" type="password" class="input w-full" :disabled="saving" :placeholder="entry.display || 'socks5h://user:password@host:port'" autocomplete="new-password" spellcheck="false" data-1p-ignore data-lpignore="true" data-bwignore="true" @input="addressChanged(entry)" />
          </div>
          <label class="flex min-h-10 items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input v-model="entry.enabled" type="checkbox" :disabled="saving" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" @change="markDirty" />
            {{ t('admin.settings.stateProxies.enabled') }}
          </label>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="saving || testing.has(entry.id) || (!entry.url?.trim() && !entry.revision)" @click="probe(entry)">{{ testing.has(entry.id) ? t('admin.settings.stateProxies.testing') : t('admin.settings.stateProxies.test') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="remove(entry.id)">{{ t('common.delete') }}</button>
          <span v-if="entry.last_acquisition" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.stateProxies.acquisition') }}: {{ acquisitionResult(entry.last_acquisition) }}</span>
        </div>
        <div class="space-y-1 break-words text-sm text-gray-600 dark:text-gray-300" aria-live="polite">
          <p v-if="entry.probe" :class="entry.probe.success ? 'text-emerald-700 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
            {{ entry.probe.success ? t('admin.settings.stateProxies.connected') : t('admin.settings.stateProxies.failed') }} · {{ entry.probe.latency_ms }} ms · {{ date(entry.probe.checked_at) }}
          </p>
          <p v-if="entry.probe?.success">{{ t('admin.settings.stateProxies.sample') }}: {{ entry.probe.ip_address || '—' }} · {{ country(entry.probe) }}</p>
          <p v-else-if="entry.last_success">{{ t('admin.settings.stateProxies.lastSuccess') }}: {{ entry.last_success.ip_address }} · {{ country(entry.last_success) }} · {{ date(entry.last_success.checked_at) }}</p>
          <p v-else-if="!testing.has(entry.id) && !entry.probe" class="text-gray-500 dark:text-gray-400">{{ t('admin.settings.stateProxies.untested') }}</p>
        </div>
      </div>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="saved" role="status" class="text-sm text-emerald-700 dark:text-emerald-400">{{ t('admin.settings.stateProxies.saved') }}</p>
    <div class="flex flex-wrap gap-2">
      <button v-if="loaded" type="button" class="btn btn-primary btn-sm" :disabled="saving || !dirty" @click="save">{{ saving ? t('common.loading') : t('admin.settings.stateProxies.save') }}</button>
      <button v-if="error" type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving" @click="load">{{ t('admin.settings.stateProxies.reload') }}</button>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getTicketProxyPool, saveTicketProxyPool, testTicketProxy, type TicketProxy, type TicketProxyProbe } from '@/api/admin/codexTicketProxies'
const { t, locale } = useI18n()
const entries = ref<TicketProxy[]>([])
const revision = ref('')
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const dirty = ref(false)
const saved = ref(false)
const error = ref('')
const testing = ref(new Set<string>())
let generation = 0
function date(value: string) { return new Date(value).toLocaleString(locale.value) }
function acquisitionResult(value: string) {
  const results: Record<string, string> = {
    network_error: 'networkError', candidate_rejected: 'candidateRejected', response_received: 'responseReceived'
  }
  return t(`admin.settings.stateProxies.${results[value] ?? 'unknownResult'}`)
}
function country(result: TicketProxyProbe) {
  if (result.country) return result.country
  if (result.country_code) {
    try { return new Intl.DisplayNames([locale.value], { type: 'region' }).of(result.country_code) ?? result.country_code } catch { return result.country_code }
  }
  return t('admin.settings.stateProxies.unknownCountry')
}
async function load() {
  const current = ++generation
  loading.value = true
  error.value = ''
  try {
    const pool = await getTicketProxyPool()
    if (generation !== current) return
    entries.value = pool.entries.map(entry => ({ ...entry, url: '' }))
    revision.value = pool.revision
    loaded.value = true
    dirty.value = false
  } catch { if (generation === current) error.value = t('admin.settings.stateProxies.loadFailed') }
  finally { if (generation === current) loading.value = false }
}
function markDirty() { dirty.value = true; saved.value = false }
function add() {
  entries.value.push({ id: crypto.randomUUID(), revision: '', name: '', display: '', enabled: true, url: '' })
  dirty.value = true
  saved.value = false
}
function remove(id: string) { entries.value = entries.value.filter(e => e.id !== id); dirty.value = true; saved.value = false }
function addressChanged(entry: TicketProxy) { entry.probe = undefined; entry.last_success = undefined; dirty.value = true; saved.value = false }
async function probe(entry: TicketProxy) {
  const current = generation
  const address = entry.url
  const entryRevision = entry.revision
  testing.value.add(entry.id)
  try {
    const result = await testTicketProxy({ ...entry })
    if (current !== generation || entry.url !== address || entry.revision !== entryRevision || !entries.value.includes(entry)) return
    entry.probe = result
    if (result.success) entry.last_success = result
  } catch {
    if (current === generation && entry.url === address && entry.revision === entryRevision) {
      entry.probe = { success: false, latency_ms: 0, checked_at: new Date().toISOString() }
    }
  } finally { testing.value.delete(entry.id) }
}
async function save() {
  if (saving.value || !dirty.value) return
  const current = ++generation
  saving.value = true
  saved.value = false
  error.value = ''
  try {
    const pool = await saveTicketProxyPool({ revision: revision.value, entries: entries.value })
    if (generation !== current) return
    entries.value = pool.entries.map(entry => ({ ...entry, url: '' }))
    revision.value = pool.revision
    dirty.value = false
    saved.value = true
  } catch (cause: unknown) {
    if (generation === current) error.value = t(((cause as { status?: number })?.status ?? (cause as { response?: { status?: number } })?.response?.status) === 409 ? 'admin.settings.stateProxies.conflict' : 'admin.settings.stateProxies.saveFailed')
  } finally { if (generation === current) saving.value = false }
}
onMounted(load)
onBeforeUnmount(() => { generation++ })
</script>
