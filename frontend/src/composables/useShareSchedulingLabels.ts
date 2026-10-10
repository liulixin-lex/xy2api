import { useI18n } from 'vue-i18n'

/** Localised labels for share scheduling codes; unknown codes show as-is. */
export function useShareSchedulingLabels() {
  const { t, te } = useI18n()
  const label = (group: string, code: string | null | undefined): string => {
    if (!code) return '-'
    const key = `admin.shareScheduling.${group}.${code}`
    if (typeof te === 'function' && !te(key)) return code
    return t(key)
  }
  return {
    reason: (code: string | null | undefined) => label('reasons', code),
    cacheClass: (code: string | null | undefined) => label('cacheClasses', code),
    keySource: (code: string | null | undefined) => label('keySources', code),
    scheduler: (code: string | null | undefined) => label('schedulers', code),
    write: (code: string | null | undefined) => label('explain.writes', code)
  }
}
