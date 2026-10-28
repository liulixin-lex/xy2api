import { computed } from 'vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId, AdminModuleId } from '@/types/adminAccess'

/**
 * Per-page permission helpers for the admin console (operator role).
 * Super admins get true everywhere; operators follow `/me/access`.
 */
export function useAdminPermission(module: AdminModuleId) {
  const access = useAdminAccessStore()
  return {
    canView: computed(() => access.can(`${module}:view`)),
    canManage: computed(() => access.can(`${module}:manage`)),
    has: (flag: AdminFlagId) => access.can(flag),
    isSuperAdmin: computed(() => access.isSuperAdmin)
  }
}
