import { onScopeDispose, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { Account } from '@/types'
import type { SchedAccountSummary } from '@/types/schedQuality'
import { useAppStore } from '@/stores/app'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { markSchedChanged } from '@/composables/useSchedLayers'
import { activeStreamOnly, type AccountWithCapability } from '@/utils/accountCapability'
import { IQ_GATE_KEY_PREFIX } from '@/components/admin/account/iqCheckPresentation'

// "Recover state" (contract delta F): one click runs the existing reset, one toast tells the result. The row menu,
// the drawer and the temporary-pause dialog share one lock per account with the batch recovery; it holds until the
// request and the refresh after it are done.

export type RecoverableRow = Pick<Account, 'status' | 'rate_limit_reset_at' | 'overload_until' | 'temp_unschedulable_until' | 'extra'> &
  Pick<AccountWithCapability, 'capability_learned'>

const CONCURRENCY = 4

/** Ends after `now`; missing or invalid is none, equal to `now` has ended. */
function activeUntil(value: string | null | undefined, now: number) {
  if (!value) return false
  const at = Date.parse(value)
  return Number.isFinite(at) && at > now
}

/**
 * Whether a recovery can clear something on this account (F1), from the list row and its quality summary only.
 * Manual off, schedulable off, quota, expiry, IQ gate, parent block, overrides and recent stats are not counted.
 * Without a summary yet, only the row decides.
 */
export function canRecover(row: RecoverableRow, summary: Pick<SchedAccountSummary, 'groups'> | undefined, now: number): boolean {
  if (row.status === 'error') return true
  if ([row.rate_limit_reset_at, row.overload_until, row.temp_unschedulable_until].some((at) => activeUntil(at, now))) return true
  const limits = row.extra?.model_rate_limits ?? {}
  if (Object.entries(limits).some(([key, info]) => !key.startsWith(IQ_GATE_KEY_PREFIX) && activeUntil(info?.rate_limit_reset_at, now))) return true
  if (activeStreamOnly(row.capability_learned, now).length) return true
  return (summary?.groups.cooling ?? 0) > 0
}

const pending = reactive(new Set<number>())
const batchRunning = ref(false)

/** Account ids being recovered, single or in a batch. */
export const isRecovering = (id: number) => pending.has(id)

export function useAccountRecovery() {
  const { t } = useI18n()
  const appStore = useAppStore()
  const { canManage } = useAdminPermission('accounts')
  let disposed = false
  onScopeDispose(() => { disposed = true })

  /** One account: the reset, one toast, then `refresh` (row, summary, list, drawer). */
  async function recover(account: { id: number; name: string }, refresh?: () => Promise<unknown> | void) {
    if (!canManage.value || pending.has(account.id)) return
    pending.add(account.id)
    try {
      let ok = false
      try {
        // Kept items (session avoidance and the like) are not failures.
        ok = (await schedQualityAPI.reset(account.id)).failed.length === 0
      } catch {
        // A request error is a failure.
      }
      if (ok) appStore.showSuccess(t('admin.schedQuality.recover.ok', { name: account.name }))
      else appStore.showError(t('admin.schedQuality.recover.failed', { name: account.name }))
      markSchedChanged()
      await refresh?.()
    } finally {
      pending.delete(account.id)
    }
  }

  /**
   * A batch, four at a time. Accounts already being recovered are left out. Dispatch stops when accounts:manage is
   * withdrawn or the page goes; answers already on their way still count. One toast with the counts, then `refresh`.
   * Returns the ids that recovered and the ids that failed; the rest were not run.
   */
  async function recoverMany(ids: number[], refresh?: () => Promise<unknown> | void) {
    const recovered: number[] = []
    const failed: number[] = []
    if (batchRunning.value || !canManage.value) return { recovered, failed }
    const queue = ids.filter((id) => !pending.has(id))
    queue.forEach((id) => pending.add(id))
    batchRunning.value = true
    let next = 0
    const worker = async () => {
      while (next < queue.length && canManage.value && !disposed) {
        const id = queue[next++]
        try {
          if ((await schedQualityAPI.reset(id)).failed.length === 0) recovered.push(id)
          else failed.push(id)
        } catch {
          failed.push(id)
        }
      }
    }
    try {
      await Promise.all(Array.from({ length: Math.min(CONCURRENCY, queue.length) }, worker))
      if (recovered.length || failed.length) {
        if (failed.length) appStore.showError(t('admin.schedQuality.recover.batchPartial', { ok: recovered.length, failed: failed.length }))
        else appStore.showSuccess(t('admin.schedQuality.recover.batchOk', { n: recovered.length }))
      }
      markSchedChanged()
      await refresh?.()
    } finally {
      queue.forEach((id) => pending.delete(id))
      batchRunning.value = false
    }
    return { recovered, failed }
  }

  return { recover, recoverMany, isRecovering, batchRunning }
}
