<template>
  <BaseDialog :show="visible" :title="t(`${key}.loginTitle`)" :close-on-escape="!session.stepUp.visible.value" @close="close">
    <div class="space-y-5">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.loginHint`) }}</p>
      <p class="text-sm text-gray-500">{{ t(`${key}.workerHint`) }}</p>
      <p v-if="session.errorKey.value" role="alert" class="text-sm text-red-600">{{ t(`${key}.errors.${session.errorKey.value}`) }}</p>
      <div v-if="!task" class="flex items-end gap-2">
        <label class="flex-1 text-sm"><span>{{ t(`${key}.resumeTask`) }}</span><input v-model.number="resumeTaskID" data-test="resume-id" type="number" min="1" step="1" class="input mt-1 w-full" :disabled="session.busy.value" /></label>
        <button data-test="resume" class="btn btn-secondary" :disabled="!Number.isInteger(resumeTaskID) || resumeTaskID < 1 || session.busy.value" @click="resumeTask">{{ t(`${key}.resume`) }}</button>
      </div>
      <form v-if="!task" class="space-y-4" autocomplete="off" @submit.prevent="startLogin">
        <label class="block text-sm"><span>{{ t(`${key}.email`) }}</span>
          <input v-model="email" data-test="email" type="email" autocomplete="off" required maxlength="254" class="input mt-1 w-full" :disabled="session.busy.value" />
        </label>
        <label class="block text-sm"><span>{{ t(`${key}.password`) }}</span>
          <input v-model="password" data-test="password" type="password" autocomplete="new-password" required maxlength="4096" class="input mt-1 w-full" :disabled="session.busy.value" />
        </label>
        <label class="block text-sm"><span>{{ t(`${key}.secret`) }}</span>
          <input v-model="secret" data-test="secret" type="password" autocomplete="off" spellcheck="false" required maxlength="512" class="input mt-1 w-full" :disabled="session.busy.value" />
        </label>
        <label class="block text-sm"><span>{{ t(`${key}.accountName`) }}</span>
          <input v-model="name" data-test="name" maxlength="100" class="input mt-1 w-full" :disabled="session.busy.value" />
        </label>
        <label class="block text-sm"><span>{{ t(`${key}.proxy`) }}</span>
          <select v-model="proxyID" data-test="proxy" class="input mt-1 w-full" :disabled="session.busy.value">
            <option :value="null">{{ t(`${key}.noProxy`) }}</option>
            <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">{{ proxy.name }}</option>
          </select>
        </label>
        <fieldset v-if="groups.length" :disabled="session.busy.value" class="space-y-2">
          <legend class="mb-1 text-sm">{{ t(`${key}.groups`) }}</legend>
          <label v-for="group in groups" :key="group.id" class="mr-4 inline-flex items-center gap-2 text-sm">
            <input v-model="groupIDs" type="checkbox" :value="group.id" />{{ group.name }}
          </label>
        </fieldset>
        <button data-test="login" type="submit" class="btn btn-primary" :disabled="!credentialsValid || session.busy.value">{{ t(`${key}.startLogin`) }}</button>
      </form>
      <div v-else class="space-y-3" aria-live="polite">
        <p data-test="task-state" class="text-sm font-medium">{{ t(`${key}.taskNumber`, { id: task.task_id }) }} · {{ t(`${key}.states.${task.state}`) }}</p>
        <p v-if="task.state === 'failed'" class="text-sm text-red-600">{{ t(`${key}.errors.loginFailed`) }} {{ t(`${key}.taskErrors.${openAITotpTaskErrorKey(task.error_code)}`) }}</p>
        <p v-if="task.state === 'ready'" class="text-sm text-gray-600 dark:text-gray-300">{{ t(`${key}.readyHint`) }}</p>
        <button v-if="task.state === 'ready'" data-test="import" class="btn btn-primary" :disabled="session.busy.value" @click="importAccount">{{ t(`${key}.importAccount`) }}</button>
        <button v-if="task.state === 'failed'" data-test="new-login" class="btn btn-secondary" :disabled="session.busy.value" @click="task = null">{{ t(`${key}.newLogin`) }}</button>
        <button v-if="isOpenAITotpTaskActive(task) || session.errorKey.value" data-test="refresh" class="btn btn-secondary" :disabled="session.busy.value" @click="refreshTask">{{ t(`${key}.refresh`) }}</button>
      </div>
      <p v-if="task && isOpenAITotpTaskActive(task)" class="text-sm text-gray-500">{{ t(`${key}.closeHint`) }}</p>
    </div>
    <template #footer><button class="btn btn-secondary" @click="close">{{ t('common.close') }}</button></template>
  </BaseDialog>
  <TotpStepUpDialog v-if="visible" :controller="session.stepUp" />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { openaiTotpAPI, type OpenAITotpTask } from '@/api/admin/openaiTotp'
import type { AdminGroup, Proxy } from '@/types'
import { isOpenAITotpTaskActive, normalizeTotpSecret, openAITotpTaskErrorKey, useOpenAITotpDialog, validTotpCredentials } from './openaiTotpDialog'

const props = withDefaults(defineProps<{ show: boolean; proxies?: Proxy[]; groups?: AdminGroup[] }>(), { proxies: () => [], groups: () => [] })
const emit = defineEmits<{ close: []; imported: [accountID: number] }>()
const { t } = useI18n()
const key = 'admin.accounts.openaiTotp'
const auth = useAuthStore()
const visible = computed(() => props.show && auth.isAdmin)
const email = ref('')
const password = ref('')
const secret = ref('')
const name = ref('')
const proxyID = ref<number | null>(null)
const groupIDs = ref<number[]>([])
const task = ref<OpenAITotpTask | null>(null)
const resumeTaskID = ref(0)
const session = useOpenAITotpDialog(() => visible.value)
const credentialsValid = computed(() => validTotpCredentials(email.value, password.value, secret.value))

function clearCredentials() { email.value = ''; password.value = ''; secret.value = '' }
function reset() {
  session.reset()
  clearCredentials()
  name.value = ''
  proxyID.value = null
  groupIDs.value = []
  task.value = null
  resumeTaskID.value = 0
}
function close() { reset(); emit('close') }

async function startLogin() {
  if (!credentialsValid.value || task.value) return
  const result = await session.run(signal => openaiTotpAPI.login({
    email: email.value.trim(), password: password.value, totp_secret: normalizeTotpSecret(secret.value),
    ...(name.value.trim() ? { name: name.value.trim() } : {}),
    ...(proxyID.value !== null ? { proxy_id: proxyID.value } : {}),
    ...(groupIDs.value.length ? { group_ids: [...groupIDs.value] } : {})
  }, signal))
  if (!result?.current()) return
  clearCredentials()
  task.value = result.value
  if (isOpenAITotpTaskActive(task.value)) session.poll(refreshTask)
}

async function resumeTask() {
  if (!Number.isInteger(resumeTaskID.value) || resumeTaskID.value < 1) return
  const result = await session.run(signal => openaiTotpAPI.loginTask(resumeTaskID.value, signal), false)
  if (!result?.current()) return
  clearCredentials()
  task.value = result.value
  if (isOpenAITotpTaskActive(task.value)) session.poll(refreshTask)
}

async function refreshTask() {
  if (!task.value) return
  session.stopPolling()
  const result = await session.run(signal => openaiTotpAPI.loginTask(task.value!.task_id, signal), false)
  if (!result?.current()) return
  task.value = result.value
  if (isOpenAITotpTaskActive(task.value)) session.poll(refreshTask)
}

async function importAccount() {
  if (task.value?.state !== 'ready') return
  const result = await session.run(signal => openaiTotpAPI.importLogin(task.value!.task_id, signal))
  if (!result?.current()) return
  const accountID = result.value.account_id
  reset()
  emit('imported', accountID)
  emit('close')
}

watch([() => props.show, () => auth.user?.id, () => auth.isAdmin], reset, { flush: 'sync' })
onUnmounted(reset)
</script>
