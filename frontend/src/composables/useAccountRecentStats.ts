import { onScopeDispose, ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AccountRecentStats } from '@/types/schedQuality'

/**
 * Recent status of the accounts on the current list page (contract delta C4). The view calls refresh() after the
 * list renders and wherever it refreshes the list; a response that arrives after a newer request is dropped, and a
 * failed request keeps the values already shown. cancel() drops the request in flight (the column was hidden).
 */
export function useAccountRecentStats() {
  const byAccount = shallowRef(new Map<number, AccountRecentStats>())
  const loading = ref(false)
  let seq = 0
  let controller: AbortController | null = null

  function cancel() {
    seq++
    controller?.abort()
    controller = null
    loading.value = false
  }

  async function refresh(accountIds: number[]) {
    cancel()
    const current = seq
    if (!accountIds.length) {
      byAccount.value = new Map()
      return
    }
    const ctrl = new AbortController()
    controller = ctrl
    loading.value = true
    try {
      const { items } = await adminAPI.accounts.getRecentStats(accountIds, { signal: ctrl.signal })
      if (current !== seq) return
      byAccount.value = new Map(items.map((item) => [item.account_id, item]))
    } catch (error) {
      if (current !== seq) return
      console.error('Failed to load recent stats:', error)
    } finally {
      if (current === seq) {
        loading.value = false
        controller = null
      }
    }
  }

  onScopeDispose(cancel)

  return { byAccount, loading, refresh, cancel }
}
