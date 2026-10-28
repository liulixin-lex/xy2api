/**
 * Operator (普通管理员) rules for account payloads and errors, shared by the
 * create, edit and bulk-edit modals (xy-admin-operator-scope).
 *
 * The backend is the security boundary. These helpers only keep an operator's
 * save from carrying fields the backend refuses, so a mixed form never fails:
 * a left-out network key or billing extra keeps its stored value.
 */

/** Account types whose tokens are refreshed: operators never change their network identity. */
export const REFRESHABLE_ACCOUNT_TYPES: readonly string[] = ['oauth', 'setup-token', 'service_account']

/** Address keys inside `credentials`. */
export const OPERATOR_ADDRESS_CREDENTIAL_KEYS = ['base_url', 'api_base_urls'] as const
/** Network identity keys inside `credentials` (address keys plus protocol, mode and region). */
export const OPERATOR_NETWORK_CREDENTIAL_KEYS = ['base_url', 'api_base_urls', 'api_protocol', 'account_mode', 'aws_region'] as const
/** Address keys inside `extra`. */
export const OPERATOR_ADDRESS_EXTRA_KEYS = ['custom_base_url', 'custom_base_url_enabled'] as const
/** Billing extras: price fields under "Adjust balance". */
export const OPERATOR_BILLING_EXTRA_KEYS = [
  'cache_ttl_override_enabled',
  'cache_ttl_override_target',
  'openai_long_context_billing_enabled'
] as const

export type OperatorPayloadMode = 'create' | 'update' | 'bulk'

export interface OperatorPayloadOptions {
  mode?: OperatorPayloadMode
  /** Update: the network identity is read-only (no address flag, or a refreshable account). Create: no address flag. */
  networkLocked: boolean
  canAdjustBalance: boolean
}

function isEmptyObject(value: unknown): boolean {
  return !!value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0
}

function withoutKeys(value: unknown, keys: readonly string[]): unknown {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value
  const copy: Record<string, unknown> = { ...(value as Record<string, unknown>) }
  for (const key of keys) delete copy[key]
  return copy
}

/**
 * Returns a copy of an operator's account payload without the fields the
 * operator may not send. Call it only for operators.
 * - Update with a locked network: no `proxy_id`, `type`, credential network keys or extra address keys.
 * - Create without the address flag: no address keys and no `proxy_id` (the platform default endpoint is used).
 * - Bulk: never any network key, flag or not.
 * - Without "Adjust balance": no `group_ids` and no billing extras.
 */
export function applyOperatorAccountPayloadRules<T extends Record<string, unknown>>(
  payload: T,
  options: OperatorPayloadOptions
): T {
  const mode = options.mode ?? 'update'
  const out: Record<string, unknown> = { ...payload }
  const stripNetwork = mode === 'bulk' || options.networkLocked
  if (stripNetwork) {
    delete out.proxy_id
    const credentialKeys = mode === 'create' ? OPERATOR_ADDRESS_CREDENTIAL_KEYS : OPERATOR_NETWORK_CREDENTIAL_KEYS
    if (mode !== 'create') delete out.type
    if ('credentials' in out) out.credentials = withoutKeys(out.credentials, credentialKeys)
    if ('credential_extras' in out) out.credential_extras = withoutKeys(out.credential_extras, credentialKeys)
    if ('extra' in out) out.extra = withoutKeys(out.extra, OPERATOR_ADDRESS_EXTRA_KEYS)
    if (mode === 'bulk') {
      delete out.base_url
      delete out.api_protocol
      delete out.account_mode
      delete out.aws_region
    }
  }
  if (!options.canAdjustBalance) {
    delete out.group_ids
    if ('extra' in out) out.extra = withoutKeys(out.extra, OPERATOR_BILLING_EXTRA_KEYS)
  }
  if (mode === 'bulk') {
    // A bulk update merges these maps: one left empty by the rules above would only be noise.
    for (const key of ['credentials', 'credential_extras', 'extra']) {
      const value = out[key]
      if (value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0 && !isEmptyObject(payload[key])) {
        delete out[key]
      }
    }
  }
  return out as T
}

interface ApiErrorShape {
  reason?: string
  metadata?: Record<string, unknown>
}

type Translate = (key: string, params?: Record<string, unknown>) => string

/**
 * Message for the operator error codes of an account, proxy or preview save,
 * or null when the error is not one of them. `genericMessage` is used for a
 * non-canonical request.
 */
export function operatorAccountErrorMessage(error: unknown, t: Translate, genericMessage?: string): string | null {
  const err = (error ?? {}) as ApiErrorShape
  const metadata = err.metadata ?? {}
  switch (err.reason) {
    case 'ACCOUNT_API_KEY_REQUIRED':
      return metadata.field === 'newapi_access_token'
        ? t('admin.operatorAccess.errors.newApiTokenRequired')
        : t('admin.operatorAccess.errors.keyRequired', { field: String(metadata.field ?? '') })
    case 'OPERATOR_NETWORK_CHANGE_FORBIDDEN':
      return t('admin.operatorAccess.errors.networkChangeForbidden')
    case 'ADMIN_FIELD_FORBIDDEN':
      if (metadata.cause === 'private_address') return t('admin.operatorAccess.errors.privateAddress')
      if (metadata.cause === 'invalid_region') return t('admin.operatorAccess.errors.invalidRegion')
      if (metadata.cause === 'proxy_in_use') return t('admin.operatorAccess.errors.proxyInUse')
      if (metadata.cause === 'noncanonical') return genericMessage ?? null
      return null
    default:
      return null
  }
}
