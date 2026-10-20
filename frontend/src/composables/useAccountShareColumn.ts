import { ref, watch } from 'vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type { ShareMember } from '@/api/admin/shareScheduling'

export type AccountShare = Pick<ShareMember, 'expected_share' | 'share_if_healthy' | 'healthy'>

/**
 * Expected shares of new sessions for the account list's "share" column.
 * The column exists only while the list is filtered to a group that runs
 * share scheduling; any other filter, or a failed read, leaves it hidden.
 */
export function useAccountShareColumn(groupFilter: () => string | number | null | undefined) {
  const shares = ref<Map<number, AccountShare> | null>(null)
  let request = 0

  async function load(value: string | number | null | undefined) {
    const current = ++request
    const groupId = Number(value)
    if (!Number.isSafeInteger(groupId) || groupId <= 0) {
      shares.value = null
      return
    }
    try {
      const view = await shareSchedulingAPI.getPolicy(groupId)
      if (current !== request) return
      shares.value = view.scheduler === 'share'
        ? new Map(view.members.map(m => [m.account_id, { expected_share: m.expected_share, share_if_healthy: m.share_if_healthy, healthy: m.healthy }]))
        : null
    } catch {
      if (current === request) shares.value = null
    }
  }

  watch(groupFilter, value => { void load(value) }, { immediate: true })

  return { shares, refresh: () => load(groupFilter()) }
}
