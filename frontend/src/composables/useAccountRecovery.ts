import { onScopeDispose, reactive, ref } from 'vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedResetResult } from '@/types/schedQuality'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { markSchedChanged } from '@/composables/useSchedLayers'

// 恢复 (FINAL §5, contract §6.5): one click or a batch, any time. Each account keeps one result line (what was
// cleared, what is left) until it is closed or the account is recovered again. The row button, the drawer and the batch
// share one lock per account; it holds until the request and the refreshes after it are done.

export interface RecoveryLine {
  /** When the answer came in (ms). */
  at: number
  /** null: the request itself failed. */
  result: SchedResetResult | null
}

const CONCURRENCY = 4

const pending = reactive(new Set<number>())
const batchRunning = ref(false)
/** Result lines by account id, shared by the list and the drawer. */
export const recoveryLines = reactive(new Map<number, RecoveryLine>())

/** Account ids being recovered, single or in a batch. */
export const isRecovering = (id: number) => pending.has(id)

/** The recovery went through with nothing failed. */
export const recoveredFully = (line: RecoveryLine) => !!line.result && line.result.failed.length === 0

async function resetOne(id: number): Promise<RecoveryLine> {
  try {
    return { at: Date.now(), result: await schedQualityAPI.reset(id) }
  } catch {
    return { at: Date.now(), result: null }
  }
}

export function useAccountRecovery() {
  const { canManage } = useAdminPermission('accounts')
  let disposed = false
  onScopeDispose(() => { disposed = true })

  /** One account: the reset, its result line, then `refresh` (the page's row and summary) with the open drawer's reload. */
  async function recover(id: number, refresh?: () => Promise<unknown> | void) {
    if (!canManage.value || pending.has(id)) return
    pending.add(id)
    try {
      recoveryLines.set(id, await resetOne(id))
      await Promise.all([markSchedChanged(), refresh?.()])
    } finally {
      pending.delete(id)
    }
  }

  /**
   * A batch, four at a time. Accounts already being recovered are left out. Dispatch stops when accounts:manage is
   * withdrawn or the page goes; answers already on their way still count. Returns the ids that recovered fully.
   */
  async function recoverMany(ids: number[], refresh?: () => Promise<unknown> | void) {
    const recovered: number[] = []
    if (batchRunning.value || !canManage.value) return recovered
    const queue = ids.filter((id) => !pending.has(id))
    queue.forEach((id) => pending.add(id))
    batchRunning.value = true
    let next = 0
    const worker = async () => {
      while (next < queue.length && canManage.value && !disposed) {
        const id = queue[next++]
        const line = await resetOne(id)
        recoveryLines.set(id, line)
        if (recoveredFully(line)) recovered.push(id)
      }
    }
    try {
      await Promise.all(Array.from({ length: Math.min(CONCURRENCY, queue.length) }, worker))
      await Promise.all([markSchedChanged(), refresh?.()])
    } finally {
      queue.forEach((id) => pending.delete(id))
      batchRunning.value = false
    }
    return recovered
  }

  return { recover, recoverMany, batchRunning }
}
