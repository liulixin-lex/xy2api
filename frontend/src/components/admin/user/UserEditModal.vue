<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.editUser')"
    width="normal"
    @close="$emit('close')"
  >
    <form v-if="user" id="edit-user-form" @submit.prevent="handleUpdateUser" class="space-y-5">
      <div>
        <label class="input-label">{{ t('admin.users.email') }}</label>
        <input v-model="form.email" type="email" class="input" />
        <p v-if="emailError" class="input-error-text" data-test="email-error">{{ emailError }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.password') }}</label>
        <div class="flex gap-2">
          <div class="relative flex-1">
            <input v-model="form.password" type="text" class="input pr-10" :placeholder="t('admin.users.enterNewPassword')" />
            <button v-if="form.password" type="button" @click="copyPassword" class="absolute right-2 top-1/2 -translate-y-1/2 rounded-lg p-1 transition-colors hover:bg-gray-100 dark:hover:bg-dark-700" :class="passwordCopied ? 'text-green-500' : 'text-gray-400'">
              <svg v-if="passwordCopied" class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" /></svg>
              <svg v-else class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0013.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 01-.75.75H9a.75.75 0 01-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 01-2.25 2.25H6.75A2.25 2.25 0 014.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 011.927-.184" /></svg>
            </button>
          </div>
          <button type="button" @click="generatePassword" class="btn btn-secondary px-3">
            <Icon name="refresh" size="md" />
          </button>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.username') }}</label>
        <input v-model="form.username" type="text" class="input" />
      </div>
      <div v-if="isSuperAdmin">
        <label class="input-label">{{ t('admin.users.form.roleLabel') }}</label>
        <Select
          v-model="form.role"
          :options="roleOptions"
          :searchable="false"
        />
      </div>
      <div v-if="isSuperAdmin && form.role === 'operator'">
        <OperatorPermissionsTable v-if="operatorPermissionsState === 'loaded'" v-model="operatorPermissions" />
        <p v-else-if="operatorPermissionsState === 'error'" class="input-error-text">{{ t('admin.operatorAccess.loadFailed') }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.notes') }}</label>
        <textarea v-model="form.notes" rows="3" class="input"></textarea>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.columns.concurrency') }}</label>
        <input
          v-model.number="form.concurrency"
          type="number"
          min="0"
          step="1"
          class="input"
          :placeholder="t('admin.users.form.concurrencyPlaceholder')"
          data-test="concurrency-input"
        />
        <p class="input-hint">{{ t('admin.users.form.concurrencyHint') }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.form.rpmLimit') }}</label>
        <input
          v-model.number="form.rpm_limit"
          type="number"
          min="0"
          step="1"
          class="input"
          :placeholder="t('admin.users.form.rpmLimitPlaceholder')"
        />
        <p class="input-hint">{{ t('admin.users.form.rpmLimitHint') }}</p>
      </div>
      <UserAttributeForm v-model="form.customAttributes" :user-id="user?.id" />
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" type="button" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="edit-user-form" :disabled="submitting" class="btn btn-primary">
          {{ submitting ? t('admin.users.updating') : t('common.update') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- 角色提升为管理员时后端要求 step-up 2FA，弹出 TOTP 验证后自动重试 -->
  <TotpStepUpDialog :controller="stepUp" />
</template>

<script setup lang="ts">
import { computed, ref, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { adminAPI } from '@/api/admin'
import type { AdminUser, UserAttributeValuesMap } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import UserAttributeForm from '@/components/user/UserAttributeForm.vue'
import Icon from '@/components/icons/Icon.vue'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import OperatorPermissionsTable from '@/components/admin/user/OperatorPermissionsTable.vue'
import { useAdminPermission } from '@/composables/useAdminPermission'
import type { AdminPermissionsWire } from '@/types/adminAccess'

const props = defineProps<{ show: boolean, user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])
const { t } = useI18n(); const appStore = useAppStore(); const { copyToClipboard } = useClipboard()

const submitting = ref(false); const passwordCopied = ref(false)
const { isSuperAdmin } = useAdminPermission('users')
const roleOptions = computed(() => [
  { value: 'user', label: t('admin.users.roles.user') },
  { value: 'operator', label: t('admin.users.roles.operator') },
  { value: 'admin', label: t('admin.users.roles.admin') }
])
const emailError = ref('')
// Operator permissions (super admins only): loaded once per opened user.
const operatorPermissions = ref<AdminPermissionsWire>({ modules: {}, flags: [] })
const operatorPermissionsState = ref<'idle' | 'loading' | 'loaded' | 'error'>('idle')
let operatorPermissionsUserId: number | null = null
let operatorPermissionsRequestVersion = 0
async function loadOperatorPermissions(userId: number) {
  if (operatorPermissionsUserId === userId && operatorPermissionsState.value !== 'error') return
  operatorPermissionsUserId = userId
  operatorPermissionsState.value = 'loading'
  const requestVersion = ++operatorPermissionsRequestVersion
  try {
    const perms = await adminAPI.users.getOperatorPermissions(userId)
    if (operatorPermissionsUserId !== userId || requestVersion !== operatorPermissionsRequestVersion) return
    operatorPermissions.value = { modules: { ...(perms?.modules ?? {}) }, flags: [...(perms?.flags ?? [])] }
    operatorPermissionsState.value = 'loaded'
  } catch {
    if (operatorPermissionsUserId === userId && requestVersion === operatorPermissionsRequestVersion) operatorPermissionsState.value = 'error'
  }
}
const form = reactive({
  email: '',
  password: '',
  username: '',
  notes: '',
  role: 'user' as AdminUser['role'],
  concurrency: 1,
  rpm_limit: 0,
  customAttributes: {} as UserAttributeValuesMap
})

// Reset the opening and user together, before starting any permission request.
// Each opening starts from stored permissions, never an earlier unsaved edit.
watch(() => [props.show, props.user] as const, ([, u]) => {
  operatorPermissionsRequestVersion++
  operatorPermissionsUserId = null
  operatorPermissionsState.value = 'idle'
  operatorPermissions.value = { modules: {}, flags: [] }
  if (u) {
    Object.assign(form, { email: u.email, password: '', username: u.username || '', notes: u.notes || '', role: u.role || 'user', concurrency: u.concurrency, rpm_limit: u.rpm_limit ?? 0, customAttributes: {} })
    passwordCopied.value = false
    emailError.value = ''
  }
}, { immediate: true })
// Load the table when the modal opens with an operator, or when the select first switches to operator.
// Post-flush keeps this after initialization regardless of the parent's prop order.
watch(
  () => [props.show, props.user?.id, form.role, isSuperAdmin.value] as const,
  ([show, userId, role, superAdmin]) => {
    if (show && userId && role === 'operator' && superAdmin) void loadOperatorPermissions(userId)
  },
  { immediate: true, flush: 'post' }
)

const generatePassword = () => {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789!@#$%^&*'
  let p = ''; for (let i = 0; i < 16; i++) p += chars.charAt(Math.floor(Math.random() * chars.length))
  form.password = p
}
const copyPassword = async () => {
  if (form.password && await copyToClipboard(form.password, t('admin.users.passwordCopied'))) {
    passwordCopied.value = true; setTimeout(() => passwordCopied.value = false, 2000)
  }
}
const stepUp = useStepUp()

const handleUpdateUser = async () => {
  if (!props.user) return
  if (!form.email.trim()) {
    appStore.showError(t('admin.users.emailRequired'))
    return
  }
  // 0 = 不限制，与网关 (AcquireUserSlot: maxConcurrency <= 0) 和批量改限额一致
  if (!Number.isInteger(form.concurrency) || form.concurrency < 0) {
    appStore.showError(t('admin.users.concurrencyNonNegative'))
    return
  }
  const userId = props.user.id
  const savesOperatorPermissions = isSuperAdmin.value && form.role === 'operator'
  if (savesOperatorPermissions && operatorPermissionsState.value !== 'loaded') {
    appStore.showError(t('admin.operatorAccess.loadFailed'))
    return
  }
  emailError.value = ''
  submitting.value = true
  try {
    const data: any = { email: form.email, username: form.username, notes: form.notes, concurrency: form.concurrency, rpm_limit: form.rpm_limit }
    // 角色只能由超级管理员修改；普通管理员提交 role 会被后端拒绝
    if (isSuperAdmin.value) data.role = form.role
    if (form.password.trim()) data.password = form.password.trim()
    // 任命普通管理员：先保存权限，再保存用户（含角色），两步都走 step-up
    if (savesOperatorPermissions) {
      const perms: AdminPermissionsWire = { modules: { ...operatorPermissions.value.modules }, flags: [...operatorPermissions.value.flags] }
      await stepUp.run(() => adminAPI.users.updateOperatorPermissions(userId, perms))
    }
    // 提升为管理员属敏感操作：后端返回 STEP_UP_REQUIRED 时弹 TOTP 验证并重试
    await stepUp.run(() => adminAPI.users.update(userId, data))
    if (Object.keys(form.customAttributes).length > 0) await adminAPI.userAttributes.updateUserAttributeValues(userId, form.customAttributes)
    appStore.showSuccess(t('admin.users.userUpdated'))
    emit('success'); emit('close')
  } catch (e: any) {
    if (isStepUpCancelled(e)) {
      // 用户主动取消二次验证：静默返回，表单保持打开。
    } else if (isStepUpBlocked(e)) {
      appStore.showError(
        stepUpBlockReason(e) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
          ? t('stepUp.adminApiKeyForbidden')
          : t('stepUp.notEnabled')
      )
    } else if (e?.reason === 'ADMIN_FIELD_FORBIDDEN' && e?.metadata?.field === 'email') {
      emailError.value = t('admin.operatorAccess.errors.reservedEmail')
    } else {
      appStore.showError(e?.message || t('admin.users.failedToUpdate'))
    }
  } finally { submitting.value = false }
}
</script>
