import { useI18n } from 'vue-i18n'
import { formatSharePercent } from '@/utils/shareScheduling'

/** A warning or attention item as the server or the member view reports it. */
export interface ShareRiskLike {
  code: string
  account_id?: number | null
  name?: string
  priority?: number | null
  value?: number | null
}

// Codes whose value is a share or a ratio rather than a plain number.
const PERCENT_VALUE = new Set(['single_account_dominant', 'high_failure'])

/** Localised labels for share scheduling codes; unknown codes show as-is. */
export function useShareSchedulingLabels() {
  const { t, te } = useI18n()
  const label = (group: string, code: string | null | undefined): string => {
    if (!code) return '-'
    const key = `admin.shareScheduling.${group}.${code}`
    if (typeof te === 'function' && !te(key)) return code
    return t(key)
  }
  const risk = (item: ShareRiskLike): string => {
    const key = `admin.shareScheduling.risks.${item.code}`
    if (typeof te === 'function' && !te(key)) return t('admin.shareScheduling.risks.unknown', { code: item.code })
    const name = item.name || (item.account_id !== undefined && item.account_id !== null ? `#${item.account_id}` : '')
    const value = item.value === undefined || item.value === null ? '' : PERCENT_VALUE.has(item.code) ? formatSharePercent(item.value) : String(item.value)
    return t(key, { name, value, priority: item.priority ?? '' })
  }
  return {
    reason: (code: string | null | undefined) => label('reasons', code),
    cacheClass: (code: string | null | undefined) => label('cacheClasses', code),
    keySource: (code: string | null | undefined) => label('keySources', code),
    scheduler: (code: string | null | undefined) => label('schedulers', code),
    write: (code: string | null | undefined) => label('explain.writes', code),
    risk
  }
}
