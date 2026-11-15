/** Short labels of an account shared by the accounts table and the quality panel. */
type Translate = (key: string) => string

const TYPE_KEYS: Record<string, string> = { oauth: 'admin.accounts.oauthType', 'setup-token': 'admin.accounts.setupToken', apikey: 'admin.accounts.apiKey' }

export function accountTypeLabel(type: string, t: Translate): string {
  if (type === 'bedrock') return 'AWS Bedrock'
  return TYPE_KEYS[type] ? t(TYPE_KEYS[type]) : type
}
