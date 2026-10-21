/**
 * Capabilities the gateway learned about an account (0.4.4 W6-lite): an
 * upstream that only accepts streaming requests is skipped for non-streaming
 * requests on that protocol until the entry expires or an admin clears it.
 * Pure helpers, shared by the account list and Explain.
 */

export interface AccountCapabilityLearned {
  protocol: string
  mode: string
  expires_at: string
}

export interface AccountWithCapability {
  id: number
  capability_learned?: AccountCapabilityLearned[] | null
}

/** Unexpired stream-only entries, latest expiry first. */
export function activeStreamOnly(learned: AccountCapabilityLearned[] | null | undefined, now = Date.now()): AccountCapabilityLearned[] {
  return (learned ?? [])
    .filter((entry) => entry.mode === 'stream_only' && Date.parse(entry.expires_at) > now)
    .sort((a, b) => Date.parse(b.expires_at) - Date.parse(a.expires_at))
}

/** Time left until the latest entry expires, in whole minutes below an hour, rounded hours above. */
export function streamOnlyRemaining(entries: AccountCapabilityLearned[], now = Date.now()): { unit: 'hours' | 'minutes'; n: number } {
  const ms = entries.length ? Date.parse(entries[0].expires_at) - now : 0
  if (ms < 3_600_000) return { unit: 'minutes', n: Math.max(1, Math.ceil(ms / 60_000)) }
  return { unit: 'hours', n: Math.round(ms / 3_600_000) }
}

export function streamOnlyProtocols(entries: AccountCapabilityLearned[]): string {
  return [...new Set(entries.map((entry) => entry.protocol))].join(', ')
}
