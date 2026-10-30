<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.createUser')"
    :width="showOperatorPermissions ? 'extra-wide' : 'normal'"
    @close="$emit('close')"
  >
    <form id="create-user-form" @submit.prevent="submit" class="space-y-5">
      <fieldset class="space-y-5" :disabled="loading || createdOperatorID !== null">
        <div>
          <label class="input-label">{{ t('admin.users.email') }}</label>
          <input v-model="form.email" type="email" required class="input" :placeholder="t('admin.users.enterEmail')" />
          <p v-if="emailError" class="input-error-text" data-test="email-error">{{ emailError }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.password') }}</label>
          <div class="flex gap-2">
            <div class="relative flex-1">
              <input v-model="form.password" type="text" required class="input pr-10" :placeholder="t('admin.users.enterPassword')" />
            </div>
            <button type="button" @click="generateRandomPassword" class="btn btn-secondary px-3">
              <Icon name="refresh" size="md" />
            </button>
          </div>
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.username') }}</label>
          <input v-model="form.username" type="text" class="input" :placeholder="t('admin.users.enterUsername')" />
        </div>
        <div v-if="isSuperAdmin">
          <label class="input-label">{{ t('admin.users.form.roleLabel') }}</label>
          <select v-model="form.role" class="input">
            <option value="user">{{ t('admin.users.roles.user') }}</option>
            <option value="operator">{{ t('admin.users.roles.operator') }}</option>
            <option value="admin">{{ t('admin.users.roles.admin') }}</option>
          </select>
        </div>
        <p v-if="!canAdjustBalance" class="input-hint" data-test="create-no-signup-grants">{{ t('admin.operatorAccess.createNoSignupGrants') }}</p>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div v-if="canAdjustBalance">
            <label class="input-label">{{ t('admin.users.columns.balance') }}</label>
            <input v-model="form.balance" type="number" step="any" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.users.columns.concurrency') }}</label>
            <input v-model.number="form.concurrency" type="number" class="input" />
          </div>
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
      </fieldset>
      <fieldset v-if="showOperatorPermissions" :disabled="loading"><OperatorPermissionsTable v-model="operatorPermissions" /></fieldset>
      <p v-if="createdOperatorID !== null" class="input-hint" data-test="operator-created">{{ t('admin.operatorAccess.createdPermissionsPending') }}</p>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" type="button" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="create-user-form" :disabled="loading" class="btn btn-primary">
          {{ loading ? t('admin.users.creating') : t('common.create') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- 创建管理员账号时后端要求 step-up 2FA，弹出 TOTP 验证后自动重试 -->
  <TotpStepUpDialog :controller="stepUp" />
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'; import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import OperatorPermissionsTable from './OperatorPermissionsTable.vue'
import type { AdminPermissionsWire } from '@/types/adminAccess'
import { useAuthStore } from '@/stores/auth'
import { useAdminPermission } from '@/composables/useAdminPermission'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits(['close', 'success']); const { t } = useI18n()
const appStore = useAppStore()

const form = reactive({ email: '', password: '', username: '', notes: '', role: 'user' as 'user' | 'operator' | 'admin', balance: '', concurrency: 1, rpm_limit: 0 })
const { isSuperAdmin, has } = useAdminPermission('users')
const canAdjustBalance = computed(() => has('users.balance_adjust'))
const emailError = ref('')
const auth = useAuthStore()
const operatorPermissions = ref<AdminPermissionsWire>({ modules: {}, flags: [] })
const createdOperatorID = ref<number | null>(null)
const showOperatorPermissions = computed(() => isSuperAdmin.value && form.role === 'operator')
let generation = 0

const stepUp = useStepUp()
const loading = ref(false)

const submit = async () => {
  if (loading.value) return
  loading.value = true
  const version = generation
  const current = () => version === generation && props.show
  try {
    const { balance: rawBalance, role, ...rest } = { ...form }
    const balance = String(rawBalance).trim()
    const payload: typeof rest & { balance?: number; role?: typeof role } = { ...rest }
    // 角色只能由超级管理员设置；初始余额需要“加减余额”权限
    if (isSuperAdmin.value) payload.role = role
    if (balance !== '' && canAdjustBalance.value) {
      payload.balance = Number(balance)
    }
    emailError.value = ''
    // 创建管理员属敏感操作：后端返回 STEP_UP_REQUIRED 时弹 TOTP 验证并重试
    if (createdOperatorID.value === null) {
      const created = await stepUp.run(() => {
        if (!current()) throw new Error('STALE_FORM')
        return adminAPI.users.create(payload)
      })
      if (!current()) return
      if (showOperatorPermissions.value) createdOperatorID.value = created.id
    }
    if (showOperatorPermissions.value && createdOperatorID.value !== null) {
      const userID = createdOperatorID.value
      const permissions: AdminPermissionsWire = { modules: { ...operatorPermissions.value.modules }, flags: [...operatorPermissions.value.flags] }
      await stepUp.run(() => {
        if (!current()) throw new Error('STALE_FORM')
        return adminAPI.users.updateOperatorPermissions(userID, permissions)
      })
      if (!current()) return
    }
    appStore.showSuccess(t('admin.users.userCreated'))
    emit('success'); emit('close')
  } catch (e: any) {
    if (!current()) return
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
      appStore.showError(e?.message || t('admin.users.failedToCreate'))
    }
  } finally { if (current()) loading.value = false }
}

function reset() {
  generation++
  stepUp.onCancel()
  loading.value = false
  createdOperatorID.value = null
  operatorPermissions.value = { modules: {}, flags: [] }
  Object.assign(form, { email: '', password: '', username: '', notes: '', role: 'user', balance: '', concurrency: 1, rpm_limit: 0 })
  emailError.value = ''
}
watch([() => props.show, () => auth.user?.id, () => auth.user?.role], reset, { flush: 'sync' })
onUnmounted(reset)

const generateRandomPassword = () => {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789!@#$%^&*'
  let p = ''; for (let i = 0; i < 16; i++) p += chars.charAt(Math.floor(Math.random() * chars.length))
  form.password = p
}
</script>
