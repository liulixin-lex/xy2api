/**
 * Capabilities the gateway learned about an account (0.4.4 W6-lite): an
 * upstream that only accepts streaming requests is skipped for non-streaming
 * requests on that protocol until the entry expires or an admin clears it.
 */

import { apiClient } from '../client'

export interface AccountCapabilityLearned {
  protocol: string
  mode: string
  expires_at: string
}

export interface AccountWithCapability {
  id: number
  capability_learned?: AccountCapabilityLearned[] | null
}

/** Unexpired stream-only entries, soonest-expiring last. */
export function activeStreamOnly(account: AccountWithCapability, now = Date.now()): AccountCapabilityLearned[] {
  return (account.capability_learned ?? [])
    .filter((entry) => entry.mode === 'stream_only' && Date.parse(entry.expires_at) > now)
    .sort((a, b) => Date.parse(b.expires_at) - Date.parse(a.expires_at))
}

export async function clearCapabilityLearning(id: number): Promise<void> {
  await apiClient.delete(`/admin/accounts/${id}/capability-learning`)
}
