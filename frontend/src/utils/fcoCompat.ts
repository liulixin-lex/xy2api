import type { FCOCompatMode } from '@/api/admin/resilience'

// Mirrors backend resilience.FCOCompatSettings and service.OpenAIFCOCompatExtraKey;
// the server stays authoritative.

export const FCO_COMPAT_EXTRA_KEY = 'openai_fco_compat_mode'
export const FCO_COMPAT_MODES: readonly FCOCompatMode[] = ['off', 'reactive', 'proactive']
export const FCO_COMPAT_MAX_ADMIN_NAMES = 64
export const FCO_COMPAT_ADMIN_NAME_PATTERN = /^[A-Za-z0-9_]{1,128}$/

/** Names owned by the built-in rules; administrators cannot add them. */
const FCO_COMPAT_RESERVED_NAMES: ReadonlySet<string> = new Set([
  'browser_info',
  'goal_objective',
  'untrusted_context',
  'ide_context',
  'untrusted_content',
  'proposed_plan',
  'terminal_info',
  'browser_tabs',
  'codex_apps_open_page'
])
const FCO_COMPAT_RESERVED_PREFIX = 'codex_apps_selected_page_part_'

/** An account either inherits the global mode or overrides it. */
export type FCOCompatAccountMode = 'inherit' | FCOCompatMode

export type FCOCompatAdminNameProblem = 'invalid' | 'reserved' | 'duplicate' | 'full'

export const isFCOCompatMode = (value: unknown): value is FCOCompatMode =>
  typeof value === 'string' && (FCO_COMPAT_MODES as readonly string[]).includes(value)

export const isFCOCompatReservedName = (name: string): boolean =>
  FCO_COMPAT_RESERVED_NAMES.has(name) || name.startsWith(FCO_COMPAT_RESERVED_PREFIX)

/** Why a name cannot be added to the list, or null when it can. Names are not trimmed. */
export const fcoCompatAdminNameProblem = (
  name: string,
  existing: readonly string[]
): FCOCompatAdminNameProblem | null => {
  if (!FCO_COMPAT_ADMIN_NAME_PATTERN.test(name)) return 'invalid'
  if (isFCOCompatReservedName(name)) return 'reserved'
  if (existing.includes(name)) return 'duplicate'
  if (existing.length >= FCO_COMPAT_MAX_ADMIN_NAMES) return 'full'
  return null
}

/** Reads the account override; a missing, null or unknown value inherits. */
export const readFCOCompatAccountMode = (
  extra: Record<string, unknown> | null | undefined
): FCOCompatAccountMode => {
  const value = extra?.[FCO_COMPAT_EXTRA_KEY]
  return isFCOCompatMode(value) ? value : 'inherit'
}

/** Writes the override into a whole extra object; inherit removes the key. */
export const applyFCOCompatAccountMode = (
  extra: Record<string, unknown>,
  mode: FCOCompatAccountMode
): void => {
  if (mode === 'inherit') {
    delete extra[FCO_COMPAT_EXTRA_KEY]
  } else {
    extra[FCO_COMPAT_EXTRA_KEY] = mode
  }
}

/** Options of the account override select, shared by the create, edit and bulk forms. */
export const fcoCompatAccountModeOptions = (
  t: (key: string) => string
): { value: FCOCompatAccountMode; label: string }[] => [
  { value: 'inherit', label: t('admin.accounts.openai.fcoCompatModeInherit') },
  { value: 'off', label: t('admin.accounts.openai.fcoCompatModeOff') },
  { value: 'reactive', label: t('admin.accounts.openai.fcoCompatModeReactive') },
  { value: 'proactive', label: t('admin.accounts.openai.fcoCompatModeProactive') }
]

/** The override exists only on OpenAI API key accounts. */
export const fcoCompatAppliesTo = (platform: unknown, type: unknown): boolean =>
  platform === 'openai' && type === 'apikey'

/**
 * Returns extra without the override when the account cannot carry it (a leftover would be written back, or
 * rejected, by a form that does not show it); returns extra unchanged otherwise.
 */
export const extraForFCOCompatAccount = <E extends Record<string, unknown> | undefined>(
  extra: E,
  platform: unknown,
  type: unknown
): E => {
  if (!extra || !(FCO_COMPAT_EXTRA_KEY in extra) || fcoCompatAppliesTo(platform, type)) return extra
  const copy: Record<string, unknown> = { ...extra }
  delete copy[FCO_COMPAT_EXTRA_KEY]
  return copy as E
}
