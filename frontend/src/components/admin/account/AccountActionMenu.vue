<template>
  <Teleport to="body">
    <div v-if="show && anchorRect">
      <!-- Backdrop: click anywhere outside to close -->
      <div class="fixed inset-0 z-[9998]" @click="emit('close')"></div>
      <div
        ref="menuRef"
        class="action-menu-content fixed z-[9999] w-52 overflow-y-auto overscroll-contain rounded-xl bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
        :style="menuStyle"
        @click.stop
      >
        <div class="py-1">
          <template v-if="account">
            <!-- 三组：检测与查看 → 账号操作 → 恢复；文字统一中性色，图标统一灰色 -->
            <button v-if="canManage" :class="ITEM" @click="$emit('test', account); $emit('close')">
              <Icon name="play" size="sm" :class="ICON" />
              {{ t('admin.accounts.testConnection') }}
            </button>
            <button :class="ITEM" @click="$emit('schedule', account); $emit('close')">
              <Icon name="clock" size="sm" :class="ICON" />
              {{ t('admin.scheduledTests.schedule') }}
            </button>
            <button v-if="canManage && account.platform === 'openai'" :class="ITEM" @click="$emit('iq-run', account); $emit('close')">
              <Icon name="beaker" size="sm" :class="ICON" />
              {{ t('admin.accounts.iqRun') }}
            </button>
            <button v-if="account.platform === 'openai'" :class="ITEM" @click="$emit('iq-records', account); $emit('close')">
              <Icon name="document" size="sm" :class="ICON" />
              {{ t('admin.accounts.iqRecords') }}
            </button>
            <button :class="ITEM" @click="$emit('stats', account); $emit('close')">
              <Icon name="chart" size="sm" :class="ICON" />
              {{ t('admin.accounts.viewStats') }}
            </button>
            <div v-if="hasAccountActions" :class="DIVIDER"></div>
            <button v-if="canDuplicate && canPlace" :class="ITEM" @click="$emit('duplicate', account); $emit('close')">
              <Icon name="copy" size="sm" :class="ICON" />
              {{ t('admin.accounts.duplicateAccount') }}
            </button>
            <!-- 影子账号不持凭据:重授权/刷新 token 对其无效(后端拒绝),故隐藏(外审 G4)。 -->
            <template v-if="canRenewCredentials">
              <button :class="ITEM" @click="$emit('reauth', account); $emit('close')">
                <Icon name="link" size="sm" :class="ICON" />
                {{ t('admin.accounts.reAuthorize') }}
              </button>
              <button :class="ITEM" @click="$emit('refresh-token', account); $emit('close')">
                <Icon name="refresh" size="sm" :class="ICON" />
                {{ t('admin.accounts.refreshToken') }}
              </button>
            </template>
            <button v-if="auth.isAdmin && isOpenAIOAuthParent" data-test="manage-openai-totp" :class="ITEM" @click="$emit('manage-openai-totp', account); $emit('close')">
              <Icon name="key" size="sm" :class="ICON" />
              {{ t('admin.accounts.openaiTotp.manageTitle') }}
            </button>
            <button v-if="isOpenAIOAuthParent && canPlace" :class="ITEM" @click="$emit('create-spark-shadow', account); $emit('close')">
              <Icon name="sparkles" size="sm" :class="ICON" />
              {{ t('admin.accounts.createSparkShadow') }}
            </button>
            <button v-if="canManage && supportsPrivacy" :class="ITEM" @click="$emit('set-privacy', account); $emit('close')">
              <Icon name="shield" size="sm" :class="ICON" />
              {{ t('admin.accounts.setPrivacy') }}
            </button>
            <div v-if="canManage && (hasRecoverableState || hasQuotaLimit)" :class="DIVIDER"></div>
            <button v-if="canManage && hasRecoverableState" :class="ITEM" @click="$emit('recover-state', account); $emit('close')">
              <Icon name="sync" size="sm" :class="ICON" />
              {{ t('admin.accounts.recoverState') }}
            </button>
            <button v-if="canManage && hasQuotaLimit" :class="ITEM" @click="$emit('reset-quota', account); $emit('close')">
              <Icon name="dollar" size="sm" :class="ICON" />
              {{ t('admin.accounts.resetQuota') }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { useResizeObserver, useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { Account } from '@/types'
import { IQ_GATE_KEY_PREFIX } from './iqCheckPresentation'
import { useAuthStore } from '@/stores/auth'
import { useAdminPermission } from '@/composables/useAdminPermission'

// qualityRecoverable：账号在已加载门槛状态的某个分组里熔断、保底保留或观察中。“恢复状态”会清除门槛熔断、清零次数，并重新开始 30 分钟观察。
const props = withDefaults(defineProps<{ show: boolean; account: Account | null; anchorRect: DOMRect | null; qualityRecoverable?: boolean }>(), { qualityRecoverable: false })
const emit = defineEmits(['iq-run', 'iq-records', 'close', 'test', 'stats', 'schedule', 'duplicate', 'reauth', 'refresh-token', 'recover-state', 'reset-quota', 'set-privacy', 'create-spark-shadow', 'manage-openai-totp'])
const { t } = useI18n()
const auth = useAuthStore()
const { canManage } = useAdminPermission('accounts')
// Account management includes group placement by duplicate and Spark shadow.
const canPlace = canManage
const menuRef = ref<HTMLElement | null>(null)
const { width: viewportWidth, height: viewportHeight } = useWindowSize()
const viewportPadding = 8
const menuPosition = ref({ top: viewportPadding, left: viewportPadding })
const menuStyle = computed(() => ({
  top: `${menuPosition.value.top}px`,
  left: `${menuPosition.value.left}px`,
  maxWidth: `${Math.max(0, viewportWidth.value - viewportPadding * 2)}px`,
  maxHeight: `${Math.max(0, viewportHeight.value - viewportPadding * 2)}px`
}))

const updatePosition = () => {
  if (!menuRef.value || !props.anchorRect) return

  const { width, height } = menuRef.value.getBoundingClientRect()
  const anchor = props.anchorRect
  const gap = 4
  const maxTop = viewportHeight.value - height - viewportPadding
  const top = anchor.bottom + gap <= maxTop
    ? anchor.bottom + gap
    : anchor.top - height - gap
  const left = viewportWidth.value < 768
    ? anchor.left + anchor.width / 2 - width / 2
    : anchor.right - width

  menuPosition.value.top = Math.max(viewportPadding, Math.min(top, maxTop))
  menuPosition.value.left = Math.max(viewportPadding, Math.min(left, viewportWidth.value - width - viewportPadding))
}

// Measure after rendering; menu items and translated labels can change its size.
watch([menuRef, () => props.anchorRect, viewportWidth, viewportHeight], updatePosition, { flush: 'post' })
useResizeObserver(menuRef, updatePosition)

const ITEM = 'flex w-full items-center gap-2 px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-dark-700'
const ICON = 'shrink-0 text-gray-400 dark:text-gray-500'
const DIVIDER = 'my-1 border-t border-gray-100 dark:border-dark-700'

const canDuplicate = computed(() => {
  if (!props.account || props.account.parent_account_id != null) return false
  return ['apikey', 'upstream', 'bedrock', 'service_account'].includes(props.account.type)
})
const isRateLimited = computed(() => {
  if (props.account?.rate_limit_reset_at && new Date(props.account.rate_limit_reset_at) > new Date()) {
    return true
  }
  const modelLimits = (props.account?.extra as Record<string, unknown> | undefined)?.model_rate_limits as
    | Record<string, { rate_limit_reset_at: string }>
    | undefined
  if (modelLimits) {
    const now = new Date()
    // IQ gates are not cleared by "recover state"; they lift only after a passing recheck.
    return Object.entries(modelLimits).some(([key, info]) => !key.startsWith(IQ_GATE_KEY_PREFIX) && new Date(info.rate_limit_reset_at) > now)
  }
  return false
})
const isOverloaded = computed(() => props.account?.overload_until && new Date(props.account.overload_until) > new Date())
const isTempUnschedulable = computed(() => props.account?.temp_unschedulable_until && new Date(props.account.temp_unschedulable_until) > new Date())
const hasRecoverableState = computed(() => {
  return props.account?.status === 'error' || Boolean(isRateLimited.value) || Boolean(isOverloaded.value) || Boolean(isTempUnschedulable.value) || props.qualityRecoverable
})
const isAntigravityOAuth = computed(() => props.account?.platform === 'antigravity' && props.account?.type === 'oauth')
const isOpenAIOAuth = computed(() => props.account?.platform === 'openai' && props.account?.type === 'oauth')
// 影子账号(链接型,持 parent_account_id)不持凭据、type 不可变,凭据/隐私类操作对其无效。
const isShadow = computed(() => props.account?.parent_account_id != null)
// A "parent" OpenAI OAuth account is one that is NOT itself a shadow (parent_account_id == null)
const isOpenAIOAuthParent = computed(() => isOpenAIOAuth.value && !isShadow.value)
const supportsPrivacy = computed(() => (isAntigravityOAuth.value || isOpenAIOAuth.value) && !isShadow.value)
const canRenewCredentials = computed(() => canManage.value && (props.account?.type === 'oauth' || props.account?.type === 'setup-token') && !isShadow.value)
// 第二组（账号操作）有任一项时才画分隔线。
const hasAccountActions = computed(() =>
  (canDuplicate.value && canPlace.value) ||
  canRenewCredentials.value ||
  (auth.isAdmin && isOpenAIOAuthParent.value) ||
  (isOpenAIOAuthParent.value && canPlace.value) ||
  (canManage.value && supportsPrivacy.value)
)
const hasQuotaLimit = computed(() => {
  return (props.account?.type === 'apikey' || props.account?.type === 'bedrock') && (
    (props.account?.quota_limit ?? 0) > 0 ||
    (props.account?.quota_daily_limit ?? 0) > 0 ||
    (props.account?.quota_weekly_limit ?? 0) > 0
  )
})

const handleKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      window.addEventListener('keydown', handleKeydown)
    } else {
      window.removeEventListener('keydown', handleKeydown)
    }
  },
  { immediate: true }
)

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown)
})
</script>
