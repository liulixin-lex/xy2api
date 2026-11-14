<template>
  <div ref="rootRef" class="relative">
    <button
      ref="triggerRef"
      type="button"
      class="tools-btn"
      :aria-expanded="open ? 'true' : 'false'"
      :aria-label="t('admin.accounts.moreActions')"
      v-tip="t('admin.accounts.moreActions')"
      data-test="account-tools"
      @click="toggle"
    >
      <Icon name="more" size="sm" class="tools-btn__icon-narrow" />
      <span class="tools-btn__label">{{ t('admin.accounts.moreActions') }}</span>
      <Icon name="chevronDown" size="xs" class="tools-btn__label" />
    </button>
    <Teleport to="body">
      <div
        v-if="open"
        ref="menuRef"
        class="fixed overflow-hidden rounded-lg border border-line-strong bg-surface-raised shadow-popover"
        :style="[panelStyle, { zIndex: layer.zIndex.value }]"
        @click.stop
      >
        <div class="overflow-y-auto p-2" :style="{ maxHeight: `${position.maxHeight}px` }">
          <template v-if="canManage">
            <button class="account-tools-menu-item" data-test="tools-bulk-edit-filtered" @click="pick('bulk-edit-filtered')">
              <span class="account-tools-menu-icon bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="edit" size="sm" /></span>
              <span class="flex-1 text-left">{{ t('admin.accounts.bulkEdit.submit') }}</span>
            </button>
            <div class="my-2 border-t border-line-row"></div>
          </template>
          <!-- 数据导入、导出与 CRS 同步只对超级管理员开放 -->
          <template v-if="isSuperAdmin">
            <div class="px-2 py-2 text-xs font-semibold uppercase tracking-wide text-fg-muted">{{ t('admin.accounts.dataActions') }}</div>
            <button class="account-tools-menu-item" @click="pick('sync-crs')">
              <span class="account-tools-menu-icon bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-300"><Icon name="sync" size="sm" /></span>
              <span class="flex-1 text-left">{{ t('admin.accounts.syncFromCrs') }}</span>
            </button>
            <button class="account-tools-menu-item" @click="pick('import')">
              <span class="account-tools-menu-icon bg-emerald-50 text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-300"><Icon name="upload" size="sm" /></span>
              <span class="flex-1 text-left">{{ t('admin.accounts.dataImport') }}</span>
            </button>
            <button v-if="isAdmin" data-test="openai-login-import" class="account-tools-menu-item" @click="pick('openai-login')">
              <span class="account-tools-menu-icon bg-amber-50 text-amber-600 dark:bg-amber-900/30 dark:text-amber-300"><Icon name="shield" size="sm" /></span>
              <span class="flex-1 text-left">{{ t('admin.accounts.openaiTotp.loginTitle') }}</span>
            </button>
            <button class="account-tools-menu-item" @click="pick('export')">
              <span class="account-tools-menu-icon bg-violet-50 text-violet-600 dark:bg-violet-900/30 dark:text-violet-300"><Icon name="download" size="sm" /></span>
              <span class="flex-1 text-left">{{ selectedCount ? t('admin.accounts.dataExportSelected') : t('admin.accounts.dataExport') }}</span>
              <span v-if="selectedCount" class="rounded-full bg-primary-100 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">
                {{ t('admin.accounts.selectedCount', { count: selectedCount }) }}
              </span>
            </button>
            <div class="my-2 border-t border-line-row"></div>
          </template>
          <div class="px-2 py-2 text-xs font-semibold uppercase tracking-wide text-fg-muted">{{ t('admin.accounts.toolActions') }}</div>
          <button class="account-tools-menu-item" @click="pick('error-passthrough')">
            <span class="account-tools-menu-icon bg-amber-50 text-amber-600 dark:bg-amber-900/30 dark:text-amber-300"><Icon name="shield" size="sm" /></span>
            <span class="flex-1 text-left">{{ t('admin.errorPassthrough.title') }}</span>
          </button>
          <button class="account-tools-menu-item" @click="pick('tls-profiles')">
            <span class="account-tools-menu-icon bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-200"><Icon name="lock" size="sm" /></span>
            <span class="flex-1 text-left">{{ t('admin.tlsFingerprintProfiles.title') }}</span>
          </button>
          <div class="my-2 border-t border-line-row"></div>
          <div class="flex items-center justify-between gap-3 px-2 py-2">
            <span class="text-xs font-semibold uppercase tracking-wide text-fg-muted">{{ t('admin.accounts.viewColumns') }}</span>
            <Icon name="grid" size="sm" class="text-fg-muted" />
          </div>
          <AccountColumnSettings
            :columns="columns"
            :is-visible="isVisible"
            :customized="customized"
            @toggle="emit('toggle-column', $event)"
            @reorder="emit('reorder-columns', $event)"
            @reset="emit('reset-columns')"
          />
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
/** The accounts toolbar's "more actions" menu: bulk edit by filter, data actions, tools and the column settings. */
import { computed, reactive, ref } from 'vue'
import { useEventListener } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import AccountColumnSettings from '@/components/admin/account/AccountColumnSettings.vue'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { vTip } from '@/utils/tip'

export type AccountToolAction = 'bulk-edit-filtered' | 'sync-crs' | 'import' | 'openai-login' | 'export' | 'error-passthrough' | 'tls-profiles'

defineProps<{
  canManage: boolean
  isSuperAdmin: boolean
  isAdmin?: boolean
  selectedCount: number
  columns: Array<{ key: string; label: string }>
  isVisible: (key: string) => boolean
  customized: boolean
}>()
const emit = defineEmits<{
  action: [action: AccountToolAction]
  'toggle-column': [key: string]
  'reorder-columns': [keys: string[]]
  'reset-columns': []
}>()
const { t } = useI18n()

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)
// A sub-layer of the overlay stack: while open, Escape closes it first, and a dialog opening above closes it.
const layer = useOverlayLayer(() => open.value, { root: menuRef, modal: false, floor: 9999, onEscape: () => { open.value = false } })
const position = reactive({ top: null as number | null, bottom: null as number | null, left: 16, width: 320, maxHeight: 0 })
const panelStyle = computed(() => ({
  top: position.top == null ? 'auto' : `${position.top}px`,
  bottom: position.bottom == null ? 'auto' : `${position.bottom}px`,
  left: `${position.left}px`,
  width: `${position.width}px`
}))
const place = () => {
  const trigger = triggerRef.value
  if (!trigger) return
  Object.assign(position, getFloatingPanelPosition(trigger.getBoundingClientRect(), document.documentElement.clientWidth || window.innerWidth, window.innerHeight))
}
const toggle = () => {
  if (!open.value) place()
  open.value = !open.value
}
const pick = (action: AccountToolAction) => {
  open.value = false
  emit('action', action)
}

useEventListener(document, 'click', (event: MouseEvent) => {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
})
useEventListener(window, 'resize', () => { if (open.value) place() })
useEventListener(window, 'scroll', () => { if (open.value) place() }, true)

defineExpose({ close: () => { open.value = false } })
</script>

<style scoped>
.tools-btn {
  display: inline-flex;
  height: var(--control-height);
  align-items: center;
  gap: 6px;
  padding: 0 12px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-medium);
  white-space: nowrap;
}
@media (hover: hover) {
  .tools-btn:hover {
    background: var(--color-bg-control-hover);
  }
}
.tools-btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.tools-btn__icon-narrow {
  display: none;
}
.account-tools-menu-item {
  @apply flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-fg-primary transition-colors hover:bg-surface-control-hover;
}
.account-tools-menu-icon {
  @apply inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md;
}
/* Below 1280px the button keeps only its icon (a secondary action, 7o-3); below 1024px it is a 40px square like the
   toolbar's other controls there. */
@media (max-width: 1279px) {
  .tools-btn {
    width: var(--control-height);
    padding: 0;
    justify-content: center;
  }
  .tools-btn__label {
    display: none;
  }
  .tools-btn__icon-narrow {
    display: inline-block;
  }
}
@media (max-width: 1023px) {
  .tools-btn {
    width: 40px;
    height: 40px;
  }
}
</style>
