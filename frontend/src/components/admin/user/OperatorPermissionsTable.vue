<template>
  <div class="rounded-xl border border-gray-200 dark:border-dark-600" data-test="operator-permissions-table">
    <div class="border-b border-gray-200 px-4 py-2 text-sm font-medium text-gray-900 dark:border-dark-600 dark:text-white">
      {{ t('admin.operatorAccess.title') }}
    </div>
    <table class="w-full text-sm">
      <thead>
        <tr class="text-left text-xs text-gray-500 dark:text-gray-400">
          <th class="px-4 py-2 font-medium">{{ t('admin.operatorAccess.module') }}</th>
          <th class="w-20 px-2 py-2 text-center font-medium">{{ t('admin.operatorAccess.view') }}</th>
          <th class="w-20 px-2 py-2 text-center font-medium">{{ t('admin.operatorAccess.manage') }}</th>
        </tr>
      </thead>
      <tbody
        v-for="moduleId in ADMIN_MODULE_IDS"
        :key="moduleId"
        class="border-t border-gray-100 dark:border-dark-700"
      >
        <tr :data-test="`operator-module-row-${moduleId}`">
          <td class="px-4 py-2 text-gray-900 dark:text-gray-100">{{ t(`admin.operatorAccess.modules.${moduleId}`) }}</td>
          <td class="px-2 py-2 text-center">
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :data-test="`operator-view-${moduleId}`"
              :aria-label="`${t(`admin.operatorAccess.modules.${moduleId}`)} ${t('admin.operatorAccess.view')}`"
              :checked="levelOf(moduleId) !== undefined"
              :disabled="levelOf(moduleId) === 'manage'"
              @change="onViewChange(moduleId, ($event.target as HTMLInputElement).checked)"
            />
          </td>
          <td class="px-2 py-2 text-center">
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :data-test="`operator-manage-${moduleId}`"
              :aria-label="`${t(`admin.operatorAccess.modules.${moduleId}`)} ${t('admin.operatorAccess.manage')}`"
              :checked="levelOf(moduleId) === 'manage'"
              @change="onManageChange(moduleId, ($event.target as HTMLInputElement).checked)"
            />
          </td>
        </tr>
        <tr
          v-if="MODULE_FLAGS[moduleId]"
          :data-test="`operator-flag-row-${MODULE_FLAGS[moduleId]!.flag}`"
        >
          <td colspan="3" class="px-4 pb-3 pt-0">
            <label
              class="flex items-start gap-2 pl-4"
              :class="levelOf(moduleId) === undefined ? 'opacity-60' : 'cursor-pointer'"
            >
              <input
                type="checkbox"
                class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                :data-test="`operator-flag-${MODULE_FLAGS[moduleId]!.flag}`"
                :checked="hasFlag(MODULE_FLAGS[moduleId]!.flag)"
                :disabled="levelOf(moduleId) === undefined"
                @change="onFlagChange(MODULE_FLAGS[moduleId]!.flag, ($event.target as HTMLInputElement).checked)"
              />
              <span>
                <span class="font-medium text-gray-900 dark:text-gray-100">{{ t(`admin.operatorAccess.flags.${MODULE_FLAGS[moduleId]!.key}.label`) }}</span>
                <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400" data-test="operator-flag-hint">
                  {{ t(`admin.operatorAccess.flags.${MODULE_FLAGS[moduleId]!.key}.hint`) }}
                </span>
              </span>
            </label>
          </td>
        </tr>
      </tbody>
      <tbody class="border-t border-gray-200 dark:border-dark-600">
        <tr data-test="operator-flag-row-data.export">
          <td colspan="3" class="px-4 py-3">
            <label class="flex cursor-pointer items-start gap-2">
              <input
                type="checkbox"
                class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                data-test="operator-flag-data.export"
                :checked="hasFlag('data.export')"
                @change="onFlagChange('data.export', ($event.target as HTMLInputElement).checked)"
              />
              <span>
                <span class="font-medium text-gray-900 dark:text-gray-100">{{ t('admin.operatorAccess.flags.dataExport.label') }}</span>
                <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400" data-test="operator-flag-hint">
                  {{ t('admin.operatorAccess.flags.dataExport.hint') }}
                </span>
              </span>
            </label>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  ADMIN_MODULE_IDS,
  type AdminFlagId,
  type AdminLevel,
  type AdminModuleId,
  type AdminPermissionsWire
} from '@/types/adminAccess'

type FlagKey = 'balanceAdjust' | 'upstreamAddress' | 'redeemIssue' | 'ordersRefund'

/** The flag that sits under its module's row (DEV_PLAN 4.2: 开关放在所在行). */
const MODULE_FLAGS: Partial<Record<AdminModuleId, { flag: AdminFlagId; key: FlagKey }>> = {
  users: { flag: 'users.balance_adjust', key: 'balanceAdjust' },
  accounts: { flag: 'accounts.upstream_address', key: 'upstreamAddress' },
  redeem_codes: { flag: 'redeem_codes.issue', key: 'redeemIssue' },
  orders: { flag: 'orders.refund', key: 'ordersRefund' }
}

const props = defineProps<{ modelValue: AdminPermissionsWire }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: AdminPermissionsWire): void }>()

const { t } = useI18n()

function levelOf(moduleId: AdminModuleId): AdminLevel | undefined {
  return props.modelValue.modules?.[moduleId]
}

function hasFlag(flag: AdminFlagId): boolean {
  return (props.modelValue.flags ?? []).includes(flag)
}

function emitValue(modules: AdminPermissionsWire['modules'], flags: AdminFlagId[]) {
  emit('update:modelValue', { modules, flags: [...new Set(flags)].sort() })
}

function setLevel(moduleId: AdminModuleId, level: AdminLevel | undefined) {
  const modules = { ...(props.modelValue.modules ?? {}) }
  let flags = [...(props.modelValue.flags ?? [])]
  if (level) {
    modules[moduleId] = level
  } else {
    delete modules[moduleId]
    // Clearing the row also clears its flag.
    const rowFlag = MODULE_FLAGS[moduleId]?.flag
    if (rowFlag) flags = flags.filter((flag) => flag !== rowFlag)
  }
  emitValue(modules, flags)
}

function onViewChange(moduleId: AdminModuleId, checked: boolean) {
  if (levelOf(moduleId) === 'manage') return // view is locked while manage is on
  setLevel(moduleId, checked ? 'view' : undefined)
}

function onManageChange(moduleId: AdminModuleId, checked: boolean) {
  // Manage also checks (and locks) view; unchecking manage keeps view.
  setLevel(moduleId, checked ? 'manage' : 'view')
}

function onFlagChange(flag: AdminFlagId, checked: boolean) {
  const current = props.modelValue.flags ?? []
  const flags = checked ? [...current, flag] : current.filter((item) => item !== flag)
  emitValue({ ...(props.modelValue.modules ?? {}) }, flags)
}
</script>
