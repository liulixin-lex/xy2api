import type { UsageResult } from '@/types'

// Words and colours of request results and attempts on the usage page (FINAL §6, contract §0, §3).

type Translate = (key: string, values?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

const RESULT_NAMES: Record<UsageResult, 'success' | 'rescued' | 'failed' | 'canceled'> = { 1: 'success', 2: 'rescued', 3: 'failed', 4: 'canceled' }
const OUTCOME_NAMES: Record<number, 'success' | 'timeout' | 'failed' | 'canceled'> = { 1: 'success', 2: 'timeout', 3: 'failed', 5: 'canceled' }

/** 成功 / 已救回 / 失败 / 已取消; a request without a result (before the upgrade) shows a dash. */
export function resultWord(result: UsageResult | null | undefined, t: Translate): string {
  return result ? t(`admin.schedQuality.usage.result.${RESULT_NAMES[result]}`) : '—'
}

/** Only a failure is red; a canceled request and a missing result are grey. */
export function resultClass(result: UsageResult | null | undefined): string {
  if (result === 3) return 'sq-cool'
  return result === 4 || !result ? 'sq-text-3' : 'sq-text'
}

/** Attempt outcome: 成功 / 首字超时 / 失败 / 已取消. */
export const outcomeWord = (outcome: number, t: Translate) =>
  OUTCOME_NAMES[outcome] ? t(`admin.schedQuality.usage.outcome.${OUTCOME_NAMES[outcome]}`) : t('admin.schedQuality.unknown')

/** Short word (≤ 4 characters) of a unified error class; unknown classes show the fallback word. */
export function errorClassWord(code: string | null, t: Translate, te: HasKey): string {
  if (!code) return '—'
  const key = `admin.schedQuality.usage.errorClass.${code}`
  return te(key) ? t(key) : t('admin.schedQuality.unknown')
}
