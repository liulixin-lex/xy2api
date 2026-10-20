import { ref, watch } from 'vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type { ShareMember } from '@/api/admin/shareScheduling'

export type AccountShare = Pick<ShareMember, 'expected_share' | 'share_if_healthy' | 'healthy'>

/** How long one read of a group serves the column. */
const ACCOUNT_SHARE_COLUMN_TTL_MS = 60_000

type Shares = Map<number, AccountShare> | null

interface Read {
  at: number
  promise: Promise<Shares>
}

/**
 * Expected shares of new sessions for the account list's "share" column.
 * The column exists only while the list is filtered to a group that runs
 * share scheduling; any other filter, or a failed read, leaves it hidden.
 *
 * A group is read when the filter picks it, then at most once a minute: the
 * policy view lists every member and may query the logs, so list reloads
 * reuse the last read of the group, whatever it said, instead of reading
 * again.
 */
export function useAccountShareColumn(groupFilter: () => string | number | null | undefined) {
  const shares = ref<Shares>(null)
  const reads = new Map<number, Read>()
  let request = 0

  function read(groupId: number): Promise<Shares> {
    const last = reads.get(groupId)
    if (last && Date.now() - last.at < ACCOUNT_SHARE_COLUMN_TTL_MS) return last.promise
    const promise = shareSchedulingAPI.getPolicy(groupId).then(
      view => (view.scheduler === 'share'
        ? new Map(view.members.map(m => [m.account_id, { expected_share: m.expected_share, share_if_healthy: m.share_if_healthy, healthy: m.healthy }]))
        : null),
      () => null
    )
    reads.set(groupId, { at: Date.now(), promise })
    return promise
  }

  async function load(value: string | number | null | undefined) {
    const current = ++request
    const groupId = Number(value)
    if (!Number.isSafeInteger(groupId) || groupId <= 0) {
      shares.value = null
      return
    }
    const result = await read(groupId)
    if (current === request) shares.value = result
  }

  watch(groupFilter, value => { void load(value) }, { immediate: true })

  return { shares, refresh: () => load(groupFilter()) }
}
