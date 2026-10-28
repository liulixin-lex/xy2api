// 「new-api 钱包余额（可选）」表单小节的凭据读写（xy-upstream-balance）。
// 令牌是敏感凭据：后端返回时已脱敏，只通过 credentials_status.has_newapi_access_token 暴露是否已配置。

export interface NewApiWalletDraft {
  /** 新填写的访问令牌；空串表示不改动 */
  accessToken: string
  /** new-api 用户 ID，只含数字；空串表示不设置 */
  userId: string
  /** 编辑时点了「清除」：提交 newapi_access_token: null */
  clearToken: boolean
}

export const NEWAPI_ACCESS_TOKEN_KEY = 'newapi_access_token'
export const NEWAPI_USER_ID_KEY = 'newapi_user_id'

export function emptyNewApiWalletDraft(): NewApiWalletDraft {
  return { accessToken: '', userId: '', clearToken: false }
}

// 与后端校验一致：用户 ID 非空时必须是 1–20 位数字，否则后端返回 400 NEWAPI_USER_ID_INVALID。
const NEWAPI_USER_ID_PATTERN = /^\d{1,20}$/

/**
 * credentials 已脱敏，令牌不会出现；只回填 newapi_user_id。
 * 已存的值原样保留（数字转字符串），不合法的值也不悄悄丢掉，由提交前的校验拦下并提示。
 */
export function newApiWalletDraftFromCredentials(credentials?: Record<string, unknown> | null): NewApiWalletDraft {
  const raw = credentials?.[NEWAPI_USER_ID_KEY]
  let userId = ''
  if (typeof raw === 'string') userId = raw
  else if (typeof raw === 'number' || typeof raw === 'bigint' || typeof raw === 'boolean') userId = String(raw)
  else if (raw != null) userId = JSON.stringify(raw)
  return { ...emptyNewApiWalletDraft(), userId }
}

/** 用户 ID 为空，或是 1–20 位数字。 */
export function isNewApiWalletUserIdValid(userId: string): boolean {
  const value = userId.trim()
  return value === '' || NEWAPI_USER_ID_PATTERN.test(value)
}

/**
 * 把小节的输入写进待提交的 credentials：
 * - 令牌：编辑时点了清除 → null；填了新值 → trim 后的字符串；否则不带这个键（后端保留原值）。
 *   创建时忽略清除，永远不会发送 null。
 * - 用户 ID：非空 → 字符串；为空 → 删除这个键（编辑时即清除原值）。
 */
export function applyNewApiWalletCredentials(
  credentials: Record<string, unknown>,
  draft: NewApiWalletDraft,
  mode: 'create' | 'edit'
): void {
  const token = draft.accessToken.trim()
  if (mode === 'edit' && draft.clearToken) {
    credentials[NEWAPI_ACCESS_TOKEN_KEY] = null
  } else if (token) {
    credentials[NEWAPI_ACCESS_TOKEN_KEY] = token
  } else {
    delete credentials[NEWAPI_ACCESS_TOKEN_KEY]
  }

  const userId = draft.userId.trim()
  if (userId) {
    credentials[NEWAPI_USER_ID_KEY] = userId
  } else {
    delete credentials[NEWAPI_USER_ID_KEY]
  }
}
