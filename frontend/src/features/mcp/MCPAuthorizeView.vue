<template>
  <AuthLayout>
    <section aria-labelledby="mcp-consent-title" :aria-busy="loading || submitting">
      <h2 id="mcp-consent-title" class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('mcp.authorizeTitle') }}</h2>
      <p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('mcp.authorizeDescription') }}</p>

      <p v-if="loading" class="mt-6 text-sm" role="status">{{ t('mcp.loading') }}</p>
      <div v-else-if="!preview" class="mt-6 space-y-4">
        <p role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
        <button type="button" class="btn btn-secondary" @click="loadPreview">{{ t('mcp.retry') }}</button>
      </div>

      <template v-else>
        <dl class="mt-6 space-y-4 text-sm">
          <div>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('mcp.client') }}</dt>
            <dd class="mt-1 break-words font-medium">{{ preview.client_name || preview.client_id }}</dd>
            <dd class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('mcp.unverifiedClient') }}</dd>
          </div>
          <div>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('mcp.signedInAs') }}</dt>
            <dd class="mt-1 break-all">{{ authStore.user?.email || authStore.user?.username }}</dd>
          </div>
          <div>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('mcp.resource') }}</dt>
            <dd class="mt-1 break-all font-mono text-xs">{{ preview.resource }}</dd>
          </div>
          <div>
            <dt class="text-gray-500 dark:text-dark-400">{{ t('mcp.redirect') }}</dt>
            <dd class="mt-1 break-all font-mono text-xs">{{ preview.redirect_uri }}</dd>
          </div>
        </dl>

        <p class="mt-5 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-100">{{ t('mcp.sharingNotice') }}</p>
        <fieldset :disabled="submitting || expired" class="mt-5 min-w-0">
          <legend class="text-sm font-semibold">{{ t('mcp.allowedTools') }}</legend>
          <p class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('mcp.permissionNotice') }}</p>
          <div class="mt-3 max-h-72 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
            <label v-for="tool in preview.tools" :key="tool" class="flex cursor-pointer items-start gap-3 border-b border-gray-100 p-3 text-sm last:border-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800">
              <input v-model="selectedTools" type="checkbox" :value="tool" class="mt-0.5 h-4 w-4 shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span class="min-w-0">
                <span class="block">{{ toolLabel(tool, t) }}</span>
                <span class="mt-0.5 block break-all font-mono text-xs text-gray-500 dark:text-dark-400">{{ tool }}</span>
              </span>
            </label>
          </div>
          <p v-if="!preview.tools.length" class="mt-2 text-sm">{{ t('mcp.noTools') }}</p>
        </fieldset>

        <p v-if="error" role="alert" class="mt-4 text-sm text-red-700 dark:text-red-300">{{ error }}</p>
        <p v-else-if="expired" role="alert" class="mt-4 text-sm text-red-700 dark:text-red-300">{{ t('mcp.expiredRequest') }}</p>
        <div class="mt-6 flex flex-wrap gap-3">
          <button type="button" class="btn btn-primary flex-1" :disabled="submitting || expired || !selectedTools.length" @click="submit(true)">{{ submitting ? t('mcp.submitting') : t('mcp.approve') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="submitting || expired" @click="submit(false)">{{ t('mcp.deny') }}</button>
        </div>
      </template>
      <RouterLink to="/profile" class="mt-6 inline-block text-sm text-primary-700 underline underline-offset-4 dark:text-primary-300">{{ t('mcp.backToProfile') }}</RouterLink>
    </section>
  </AuthLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthLayout from '@/components/layout/AuthLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { decideConsent, previewConsent, verifiedConsentRedirect, type ConsentPreview } from './api'
import { toolLabel } from './toolLabels'

const { t } = useI18n()
const route = useRoute()
const authStore = useAuthStore()
const preview = ref<ConsentPreview | null>(null)
const selectedTools = ref<string[]>([])
const loading = ref(false)
const submitting = ref(false)
const expired = ref(false)
const error = ref('')
const requestID = computed(() => typeof route.query.request_id === 'string' ? route.query.request_id : '')
let controller: AbortController | undefined
let expiryTimer: ReturnType<typeof setTimeout> | undefined

function clearPending() {
  controller?.abort()
  if (expiryTimer) clearTimeout(expiryTimer)
}

async function loadPreview() {
  clearPending()
  const active = new AbortController()
  controller = active
  preview.value = null
  selectedTools.value = []
  expired.value = false
  error.value = ''
  submitting.value = false
  loading.value = false
  if (!requestID.value || !authStore.user?.id) {
    error.value = t('mcp.invalidRequest')
    return
  }
  loading.value = true
  try {
    const result = await previewConsent(requestID.value, active.signal)
    if (active.signal.aborted) return
    if (result.request_id !== requestID.value) throw new Error('Mismatched authorization request')
    const remaining = Date.parse(result.expires_at) - Date.now()
    if (!Number.isFinite(remaining) || remaining <= 0) {
      error.value = t('mcp.expiredRequest')
      return
    }
    preview.value = result
    selectedTools.value = [...result.tools]
    expiryTimer = setTimeout(() => { expired.value = true }, Math.min(remaining, 2_147_483_647))
  } catch {
    if (!active.signal.aborted) error.value = t('mcp.previewFailed')
  } finally {
    if (!active.signal.aborted) loading.value = false
  }
}

async function submit(approve: boolean) {
  const current = preview.value
  if (!current || expired.value || submitting.value || (approve && !selectedTools.value.length)) return
  if (Date.parse(current.expires_at) <= Date.now()) {
    expired.value = true
    return
  }
  const active = new AbortController()
  controller?.abort()
  controller = active
  submitting.value = true
  error.value = ''
  try {
    const result = await decideConsent(current.request_id, approve, [...selectedTools.value], active.signal)
    if (active.signal.aborted) return
    const redirect = verifiedConsentRedirect(result.redirect_url, current.redirect_uri)
    if (!redirect) {
      error.value = t('mcp.redirectFailed')
      expired.value = true
      return
    }
    window.location.assign(redirect)
  } catch {
    if (!active.signal.aborted) error.value = t('mcp.decisionFailed')
  } finally {
    if (!active.signal.aborted) submitting.value = false
  }
}

watch([requestID, () => authStore.user?.id], loadPreview, { immediate: true })
onBeforeUnmount(clearPending)
</script>
