<template>
  <section v-if="visible" class="card" aria-labelledby="mcp-connections-title" :aria-busy="loading">
    <div class="border-b border-gray-200 px-6 py-4 dark:border-dark-700">
      <h2 id="mcp-connections-title" class="text-lg font-medium text-gray-900 dark:text-white">{{ t('mcp.connectionsTitle') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('mcp.connectionsDescription') }}</p>
    </div>
    <div class="space-y-5 p-6">
      <p v-if="loading" role="status" class="text-sm">{{ t('mcp.loading') }}</p>
      <div v-else-if="loadError" class="space-y-3">
        <p role="alert" class="text-sm text-red-700 dark:text-red-300">{{ t('mcp.connectionsFailed') }}</p>
        <button type="button" class="btn btn-secondary" @click="load">{{ t('mcp.retry') }}</button>
      </div>
      <template v-else>
        <div v-if="resource" class="space-y-2">
          <p class="text-sm font-medium">{{ t('mcp.serverAddress') }}</p>
          <div class="flex flex-wrap items-center gap-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <code class="min-w-0 flex-1 break-all text-xs">{{ resource }}</code>
            <button type="button" class="btn btn-secondary shrink-0" @click="copyResource">{{ copied ? t('mcp.copied') : t('mcp.copy') }}</button>
          </div>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('mcp.connectHint') }}</p>
          <p v-if="copyFailed" role="status" class="text-sm text-red-700 dark:text-red-300">{{ t('mcp.copyFailed') }}</p>
        </div>
        <p v-if="!connections.length" class="py-3 text-sm text-gray-600 dark:text-dark-300">{{ t('mcp.noConnections') }}</p>
        <ul v-else class="divide-y divide-gray-200 dark:divide-dark-700">
          <li v-for="connection in connections" :key="connection.id" class="py-5 first:pt-0 last:pb-0">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0">
                <h3 class="break-words font-medium">{{ connection.client_name || connection.client_id }}</h3>
                <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-dark-400">{{ connection.client_id }}</p>
              </div>
              <span class="rounded-md bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-dark-200">{{ t(`mcp.status.${connectionStatus(connection)}`) }}</span>
            </div>
            <dl class="mt-3 grid gap-2 text-xs text-gray-600 sm:grid-cols-2 dark:text-dark-300">
              <div><dt class="inline">{{ t('mcp.createdAt') }}: </dt><dd class="inline">{{ formatTime(connection.created_at) }}</dd></div>
              <div><dt class="inline">{{ t('mcp.expiresAt') }}: </dt><dd class="inline">{{ formatTime(connection.expires_at) }}</dd></div>
            </dl>
            <details class="mt-3 text-sm">
              <summary class="cursor-pointer py-1 text-primary-700 dark:text-primary-300">{{ t('mcp.toolCount', { count: connection.tools.length }) }}</summary>
              <ul class="mt-2 space-y-1 pl-4 text-gray-600 dark:text-dark-300">
                <li v-for="tool in connection.tools" :key="tool" class="break-words">{{ toolLabel(tool, t) }}</li>
              </ul>
            </details>
            <div v-if="connectionStatus(connection) === 'active'" class="mt-3">
              <template v-if="confirmID === connection.id">
                <p class="text-sm">{{ t('mcp.revokeNotice') }}</p>
                <div class="mt-3 flex flex-wrap gap-3">
                  <button type="button" class="btn btn-secondary text-red-700 dark:text-red-300" :disabled="revoking" @click="revoke(connection)">{{ revoking ? t('mcp.revoking') : t('mcp.confirmRevoke') }}</button>
                  <button type="button" class="btn btn-ghost" :disabled="revoking" @click="confirmID = ''; revokeFailed = false">{{ t('mcp.cancel') }}</button>
                </div>
                <p v-if="revokeFailed" role="alert" class="mt-2 text-sm text-red-700 dark:text-red-300">{{ t('mcp.revokeFailed') }}</p>
              </template>
              <button v-else type="button" class="btn btn-ghost text-red-700 dark:text-red-300" :disabled="revoking" @click="confirmID = connection.id; revokeFailed = false">{{ t('mcp.revoke') }}</button>
            </div>
          </li>
        </ul>
        <p v-if="revoked" role="status" class="text-sm text-gray-700 dark:text-dark-200">{{ t('mcp.revoked') }}</p>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { errorStatus, listConnections, revokeConnection, type MCPConnection } from './api'
import { toolLabel } from './toolLabels'

const { t, locale } = useI18n()
const authStore = useAuthStore()
const visible = ref(false)
const loading = ref(false)
const loadError = ref(false)
const connections = ref<MCPConnection[]>([])
const resource = ref('')
const confirmID = ref('')
const revoking = ref(false)
const revokeFailed = ref(false)
const revoked = ref(false)
const copied = ref(false)
const copyFailed = ref(false)
const now = ref(Date.now())
let loadController: AbortController | undefined
let revokeController: AbortController | undefined
let copyTimer: ReturnType<typeof setTimeout> | undefined
const clockTimer = setInterval(() => { now.value = Date.now() }, 60_000)

function connectionStatus(connection: MCPConnection) {
  if (connection.revoked_at) return 'revoked'
  return Date.parse(connection.expires_at) > now.value ? 'active' : 'expired'
}

function formatTime(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value) : '—'
}

async function load() {
  loadController?.abort()
  revokeController?.abort()
  const active = new AbortController()
  loadController = active
  connections.value = []
  resource.value = ''
  confirmID.value = ''
  revoked.value = false
  revoking.value = false
  loadError.value = false
  copied.value = false
  copyFailed.value = false
  visible.value = Boolean(authStore.user?.id)
  loading.value = false
  if (!visible.value) return
  loading.value = true
  try {
    const result = await listConnections(active.signal)
    if (active.signal.aborted) return
    connections.value = result.items
    resource.value = result.resource
  } catch (error) {
    if (active.signal.aborted) return
    if (errorStatus(error) === 404) visible.value = false
    else loadError.value = true
  } finally {
    if (!active.signal.aborted) loading.value = false
  }
}

async function revoke(connection: MCPConnection) {
  if (revoking.value || confirmID.value !== connection.id) return
  const active = new AbortController()
  revokeController = active
  revoking.value = true
  revokeFailed.value = false
  revoked.value = false
  try {
    await revokeConnection(connection.id, active.signal)
    if (active.signal.aborted) return
    connection.revoked_at = new Date().toISOString()
    revoked.value = true
    confirmID.value = ''
  } catch {
    if (!active.signal.aborted) revokeFailed.value = true
  } finally {
    if (!active.signal.aborted) revoking.value = false
  }
}

async function copyResource() {
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(resource.value)
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 2_000)
  } catch {
    copyFailed.value = true
  }
}

watch(() => authStore.user?.id, load, { immediate: true })
onBeforeUnmount(() => {
  loadController?.abort()
  revokeController?.abort()
  clearInterval(clockTimer)
  if (copyTimer) clearTimeout(copyTimer)
})
</script>
