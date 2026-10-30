<template>
  <BaseDialog :show="visible" :title="t(`${key}.manageTitle`)" :close-on-escape="!session.stepUp.visible.value" @close="close">
    <div class="space-y-5">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ account?.name }}</p>
      <p v-if="session.errorKey.value" role="alert" class="text-sm text-red-600">{{ t(`${key}.errors.${session.errorKey.value}`) }}</p>
      <button data-test="refresh" class="btn btn-secondary" :disabled="session.busy.value" @click="refreshStatus">{{ t(`${key}.refresh`) }}</button>
      <template v-if="status">
        <p class="text-sm">{{ t(status.configured ? `${key}.configured` : `${key}.notConfigured`) }}<span v-if="status.email"> · {{ status.email }}</span></p>
        <p v-if="!status.worker_ready" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t(`${key}.errors.workerUnavailable`) }}</p>
        <p v-if="status.locked" class="text-sm text-amber-700 dark:text-amber-300">{{ t(`${key}.lockedHint`) }}</p>
        <div v-if="status.task" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-700" aria-live="polite">
          <p data-test="task-state" class="text-sm font-medium">{{ t(`${key}.taskNumber`, { id: status.task.task_id }) }} · {{ t(`${key}.states.${status.task.state}`) }}</p>
          <p v-if="status.task.state === 'failed'" class="text-sm text-red-600">{{ t(`${key}.errors.rotationFailed`) }} {{ t(`${key}.taskErrors.${openAITotpTaskErrorKey(status.task.error_code)}`) }}</p>
          <p v-if="uncertain && status.task.error_code" class="text-sm text-amber-700 dark:text-amber-300">{{ t(`${key}.taskErrors.${openAITotpTaskErrorKey(status.task.error_code)}`) }}</p>
          <template v-if="uncertain">
            <p class="text-sm text-amber-700 dark:text-amber-300">{{ t(`${key}.uncertainHint`) }}</p>
            <div class="flex flex-wrap gap-2">
              <button data-test="verify-new" class="btn btn-primary" :disabled="!status.worker_ready || !status.task.has_candidate || session.busy.value" @click="reconcile('verify_new')">{{ t(`${key}.verifyNew`) }}</button>
              <button data-test="verify-old" class="btn btn-secondary" :disabled="!status.worker_ready || session.busy.value" @click="reconcile('verify_old')">{{ t(`${key}.verifyOld`) }}</button>
            </div>
            <p v-if="!status.task.has_candidate" class="text-sm text-gray-500">{{ t(`${key}.candidateMissing`) }}</p>
          </template>
        </div>
        <form class="space-y-4" autocomplete="off" @submit.prevent="save">
          <fieldset class="space-y-4" :disabled="blocked || session.busy.value">
            <legend class="mb-2 text-sm font-semibold">{{ t(`${key}.credentialsTitle`) }}</legend>
            <p class="text-sm text-gray-500">{{ t(`${key}.credentialsHint`) }}</p>
            <label class="block text-sm"><span>{{ t(`${key}.email`) }}</span>
              <input v-model="email" data-test="email" type="email" autocomplete="off" required maxlength="254" class="input mt-1 w-full" />
            </label>
            <label class="block text-sm"><span>{{ t(`${key}.password`) }}</span>
              <input v-model="password" data-test="password" type="password" autocomplete="new-password" required maxlength="4096" class="input mt-1 w-full" />
            </label>
            <label class="block text-sm"><span>{{ t(`${key}.secret`) }}</span>
              <input v-model="secret" data-test="secret" type="password" autocomplete="off" spellcheck="false" required maxlength="512" class="input mt-1 w-full" />
            </label>
            <button data-test="save" type="submit" class="btn btn-primary" :disabled="!credentialsValid">{{ t(`${key}.saveCredentials`) }}</button>
          </fieldset>
        </form>
        <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700">
          <p class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.rotateHint`) }}</p>
          <label class="flex items-center gap-2 text-sm"><input v-model="rotationConfirmed" data-test="rotation-confirm" type="checkbox" :disabled="blocked || session.busy.value" />{{ t(`${key}.rotateConfirm`) }}</label>
          <button data-test="rotate" class="btn btn-primary" :disabled="blocked || !status.configured || !status.worker_ready || !rotationConfirmed || session.busy.value" @click="rotate">{{ t(`${key}.rotate`) }}</button>
        </div>
        <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700">
          <h4 class="text-sm font-semibold">{{ t(`${key}.exportTitle`) }}</h4>
          <p class="text-sm text-gray-500">{{ t(`${key}.exportHint`) }}</p>
          <label class="block text-sm"><span>{{ t(`${key}.exportSource`) }}</span>
            <select v-model="exportSource" data-test="export-source" class="input mt-1 w-full" :disabled="session.busy.value">
              <option v-if="!blocked" value="current">{{ t(`${key}.sources.current`) }}</option>
              <template v-if="uncertain">
                <option :disabled="!status.task?.has_candidate" value="candidate">{{ t(`${key}.sources.candidate`) }}</option>
                <option value="previous">{{ t(`${key}.sources.previous`) }}</option>
              </template>
            </select>
          </label>
          <label class="flex items-center gap-2 text-sm"><input v-model="includePassword" data-test="include-password" type="checkbox" :disabled="session.busy.value" />{{ t(`${key}.includePassword`) }}</label>
          <button data-test="export" class="btn btn-secondary" :disabled="!canExport || session.busy.value" @click="requestExport">{{ t(`${key}.revealExport`) }}</button>
          <div v-if="exported" data-test="exported" class="space-y-3 rounded-lg border border-amber-200 p-3 dark:border-amber-900">
            <p class="text-sm">{{ exported.email }} · {{ t(`${key}.sources.${exported.source}`) }}</p>
            <p class="break-all font-mono text-sm" data-test="export-secret">{{ exported.secret }}</p>
            <label class="flex items-center gap-2 text-sm"><input v-model="showQR" data-test="show-qr" type="checkbox" />{{ t(`${key}.showQR`) }}</label>
            <img v-if="qrCode" :src="qrCode" :alt="t(`${key}.qrAlt`)" width="200" height="200" />
            <div class="flex flex-wrap gap-2">
              <button data-test="download" class="btn btn-primary" @click="download">{{ t(`${key}.downloadJSON`) }}</button>
              <button class="btn btn-secondary" @click="clearExport">{{ t(`${key}.hideExport`) }}</button>
            </div>
          </div>
        </div>
      </template>
    </div>
    <template #footer><button class="btn btn-secondary" @click="close">{{ t('common.close') }}</button></template>
  </BaseDialog>
  <TotpStepUpDialog v-if="visible" :controller="session.stepUp" />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { openaiTotpAPI, type OpenAITotpExport, type OpenAITotpExportSource, type OpenAITotpStatus } from '@/api/admin/openaiTotp'
import type { Account } from '@/types'
import { isOpenAITotpTaskActive, normalizeTotpSecret, openAITotpTaskErrorKey, useOpenAITotpDialog, validTotpCredentials } from './openaiTotpDialog'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const { t } = useI18n()
const key = 'admin.accounts.openaiTotp'
const auth = useAuthStore()
const visible = computed(() => props.show && auth.isAdmin && props.account?.platform === 'openai' && props.account?.type === 'oauth' && props.account?.parent_account_id == null)
const session = useOpenAITotpDialog(() => Boolean(visible.value))
const status = ref<OpenAITotpStatus | null>(null)
const email = ref('')
const password = ref('')
const secret = ref('')
const rotationConfirmed = ref(false)
const exportSource = ref<OpenAITotpExportSource>('current')
const includePassword = ref(false)
const exported = ref<OpenAITotpExport | null>(null)
const showQR = ref(false)
const qrCode = ref('')
let qrGeneration = 0
const uncertain = computed(() => status.value?.task?.state === 'uncertain')
const blocked = computed(() => Boolean(status.value?.locked || uncertain.value || isOpenAITotpTaskActive(status.value?.task)))
const credentialsValid = computed(() => validTotpCredentials(email.value, password.value, secret.value))
const canExport = computed(() => status.value?.configured && (exportSource.value === 'current'
  ? !blocked.value
  : uncertain.value && (exportSource.value === 'previous' || status.value.task?.has_candidate)))

function clearCredentials() { email.value = ''; password.value = ''; secret.value = '' }
function clearExport() { qrGeneration++; exported.value = null; showQR.value = false; qrCode.value = '' }
function reset() {
  session.reset()
  clearCredentials()
  clearExport()
  status.value = null
  rotationConfirmed.value = false
  exportSource.value = 'current'
  includePassword.value = false
}
function close() { reset(); emit('close') }
function applyStatus(value: OpenAITotpStatus) {
  // A task changing phase may invalidate previously exported material.
  if (status.value?.revision !== value.revision || status.value?.task?.state !== value.task?.state || status.value?.task?.task_id !== value.task?.task_id) clearExport()
  status.value = value
  if (!email.value) email.value = value.email
  if (uncertain.value && exportSource.value === 'current') exportSource.value = value.task?.has_candidate ? 'candidate' : 'previous'
  if (!uncertain.value) exportSource.value = 'current'
  if (isOpenAITotpTaskActive(value.task)) session.poll(refreshStatus)
}
async function refreshStatus() {
  session.stopPolling()
  const result = await session.run(signal => openaiTotpAPI.status(props.account!.id, signal), false)
  if (result?.current()) applyStatus(result.value)
}
async function save() {
  if (!status.value || blocked.value || !credentialsValid.value) return
  clearExport()
  const result = await session.run(signal => openaiTotpAPI.save(props.account!.id, {
    email: email.value.trim(), password: password.value, totp_secret: normalizeTotpSecret(secret.value), revision: status.value!.revision
  }, signal))
  if (!result?.current()) return
  clearCredentials()
  applyStatus(result.value)
  emit('updated')
}
async function rotate() {
  if (!status.value?.configured || blocked.value || !status.value.worker_ready || !rotationConfirmed.value) return
  clearCredentials()
  clearExport()
  const result = await session.run(signal => openaiTotpAPI.rotate(props.account!.id, signal))
  if (!result?.current()) return
  rotationConfirmed.value = false
  applyStatus({ ...status.value!, locked: isOpenAITotpTaskActive(result.value) || result.value.state === 'uncertain', task: result.value })
  await refreshStatus()
}
async function reconcile(action: 'verify_new' | 'verify_old') {
  if (!uncertain.value || !status.value?.worker_ready || (action === 'verify_new' && !status.value.task?.has_candidate)) return
  clearExport()
  const result = await session.run(signal => openaiTotpAPI.reconcile(props.account!.id, status.value!.task!.task_id, action, signal))
  if (!result?.current()) return
  if (result.value?.task_id) applyStatus({ ...status.value!, task: result.value })
  await refreshStatus()
}
async function requestExport() {
  if (!canExport.value) return
  clearExport()
  const result = await session.run(signal => openaiTotpAPI.export(props.account!.id, exportSource.value, includePassword.value, signal))
  if (!result?.current()) return
  const { password: exportedPassword, ...safe } = result.value
  exported.value = { ...safe, ...(includePassword.value && exportedPassword !== undefined ? { password: exportedPassword } : {}) }
}
function download() {
  if (!exported.value || !visible.value) return
  const url = URL.createObjectURL(new Blob([JSON.stringify(exported.value, null, 2)], { type: 'application/json' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `openai-2fa-${props.account!.id}-${exported.value.source}.json`
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}
watch([exportSource, includePassword], clearExport, { flush: 'sync' })
watch(showQR, async show => {
  const version = ++qrGeneration
  qrCode.value = ''
  if (!show || !exported.value) return
  const scope = session.capture()
  try {
    const image = await QRCode.toDataURL(exported.value.otpauth_uri, { width: 200, margin: 2 })
    if (scope.current() && version === qrGeneration && showQR.value) qrCode.value = image
  } catch {
    if (scope.current() && version === qrGeneration) session.errorKey.value = 'qrFailed'
  }
})
watch([() => props.show, () => props.account?.id, () => auth.user?.id, () => auth.isAdmin], () => {
  reset()
  if (visible.value) void refreshStatus()
}, { immediate: true, flush: 'sync' })
onUnmounted(reset)
</script>
