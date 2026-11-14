<template>
  <Teleport to="body">
    <div v-if="show && anchorRect" ref="rootRef">
      <!-- Backdrop: click anywhere outside to close -->
      <div class="fixed inset-0" :style="{ zIndex: layer.zIndex.value }" @click="emit('close')"></div>
      <div
        ref="menuRef"
        class="action-menu-content fixed z-[9999] w-52 overflow-y-auto overscroll-contain rounded-xl bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
        :style="[menuStyle, { zIndex: (layer.zIndex.value ?? 9998) + 1 }]"
        @click.stop
      >
        <div class="py-1">
          <template v-if="account">
            <!-- 恢复状态只在恢复解得了某个状态时出现，排第一；其后检测与查看 → 账号操作 → 删除。文字统一中性色，图标统一灰色 -->
            <template v-if="canManage && recoverable">
              <button
                :class="[ITEM, 'aria-disabled:cursor-not-allowed aria-disabled:opacity-50']"
                :aria-disabled="recovering || undefined"
                data-test="menu-recover-state"
                @click="onRecover"
              >
                <Icon name="sync" size="sm" :class="ICON" />
                {{ t('admin.accounts.recoverState') }}
              </button>
              <div :class="DIVIDER"></div>
            </template>
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
            <button v-if="canManage && hasQuotaLimit" :class="ITEM" @click="$emit('reset-quota', account); $emit('close')">
              <Icon name="dollar" size="sm" :class="ICON" />
              {{ t('admin.accounts.resetQuota') }}
            </button>
            <template v-if="canManage">
              <div :class="DIVIDER"></div>
              <button :class="[ITEM, '!text-red-600 dark:!text-red-400']" data-test="menu-delete" @click="$emit('delete', account); $emit('close')">
                <Icon name="trash" size="sm" class="shrink-0 text-red-500 dark:text-red-400" />
                {{ t('common.delete') }}
              </button>
            </template>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { useResizeObserver, useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { Account } from '@/types'
import { useAuthStore } from '@/stores/auth'
import { useAdminPermission } from '@/composables/useAdminPermission'

const props = defineProps<{
  show: boolean
  account: Account | null
  anchorRect: DOMRect | null
  /** A recovery can clear something on this account (contract delta F1). */
  recoverable?: boolean
  /** Its recovery is still running; the item stays first but does nothing. */
  recovering?: boolean
}>()
const emit = defineEmits(['iq-run', 'iq-records', 'close', 'test', 'stats', 'schedule', 'duplicate', 'reauth', 'refresh-token', 'recover-state', 'reset-quota', 'set-privacy', 'create-spark-shadow', 'manage-openai-totp', 'delete'])
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
const isAntigravityOAuth = computed(() => props.account?.platform === 'antigravity' && props.account?.type === 'oauth')
const isOpenAIOAuth = computed(() => props.account?.platform === 'openai' && props.account?.type === 'oauth')
// 影子账号(链接型,持 parent_account_id)不持凭据、type 不可变,凭据/隐私类操作对其无效。
const isShadow = computed(() => props.account?.parent_account_id != null)
// A "parent" OpenAI OAuth account is one that is NOT itself a shadow (parent_account_id == null)
const isOpenAIOAuthParent = computed(() => isOpenAIOAuth.value && !isShadow.value)
const supportsPrivacy = computed(() => (isAntigravityOAuth.value || isOpenAIOAuth.value) && !isShadow.value)
const canRenewCredentials = computed(() => canManage.value && (props.account?.type === 'oauth' || props.account?.type === 'setup-token') && !isShadow.value)
const hasQuotaLimit = computed(() => {
  return (props.account?.type === 'apikey' || props.account?.type === 'bedrock') && (
    (props.account?.quota_limit ?? 0) > 0 ||
    (props.account?.quota_daily_limit ?? 0) > 0 ||
    (props.account?.quota_weekly_limit ?? 0) > 0
  )
})
// 第二组（账号操作）有任一项时才画分隔线。
const hasAccountActions = computed(() =>
  (canDuplicate.value && canPlace.value) ||
  canRenewCredentials.value ||
  (auth.isAdmin && isOpenAIOAuthParent.value) ||
  (isOpenAIOAuthParent.value && canPlace.value) ||
  (canManage.value && supportsPrivacy.value) ||
  (canManage.value && hasQuotaLimit.value)
)

function onRecover() {
  if (!props.recovering) emit('recover-state', props.account)
  emit('close')
}

// A sub-layer of the overlay stack (design spec 0.2): while open, Escape closes it before the layer or page
// below, re-renders below keep their hands off its focus, and a dialog opening above closes it.
const rootRef = ref<HTMLElement | null>(null)
const layer = useOverlayLayer(() => props.show && !!props.anchorRect, { root: rootRef, modal: false, floor: 9998, onEscape: () => emit('close') })
</script>
