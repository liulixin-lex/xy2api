import { fieldKind, idRef } from './schema'
import type { SchemaField } from './types'

type Translate = (key: string, params?: Record<string, unknown>) => string
type Exists = (key: string) => boolean

const P = 'admin.cacheSettings'

/** Translates an enum-like code, falling back to the raw value for codes this build does not know. */
export function codeLabel(group: string, code: string, t: Translate, te: Exists, params?: Record<string, unknown>): string {
  const key = `${P}.${group}.${code.replace(/\+/g, '_plus_')}`
  return te(key) ? t(key, params) : code
}

export function formatValue(
  field: SchemaField | undefined,
  value: unknown,
  t: Translate,
  te: Exists,
  names: { group: (id: number) => string; account: (id: number) => string } = { group: (id) => `#${id}`, account: (id) => `#${id}` },
): string {
  if (value === undefined || value === null) return '-'
  const kind = field ? fieldKind(field) : 'unknown'
  if (kind === 'bool' || typeof value === 'boolean') return value === true ? t(`${P}.values.on`) : t(`${P}.values.off`)
  if (kind === 'int' && typeof value === 'number') {
    if (field?.unit === 'percent') return `${value}%`
    return field?.unit ? `${value} ${field.unit}` : String(value)
  }
  if (kind === 'enum') return codeLabel('options', String(value), t, te)
  if (Array.isArray(value)) {
    if (!value.length) return t(`${P}.values.empty`)
    if (kind === 'id_list') {
      const ref = field ? idRef(field) : null
      return value.map((id) => (typeof id === 'number' ? (ref === 'account' ? names.account(id) : names.group(id)) : String(id))).join(', ')
    }
    if (kind === 'enum_list') return value.map((v) => codeLabel('options', String(v), t, te)).join(', ')
    return `${value.length}`
  }
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

export function formatUtc(date: string | Date | null | undefined): string {
  if (!date) return '-'
  const d = typeof date === 'string' ? new Date(date) : date
  if (Number.isNaN(d.getTime())) return '-'
  return `${d.toISOString().slice(0, 19).replace('T', ' ')} UTC`
}

export function formatUtcClock(date: Date): string {
  return date.toISOString().slice(11, 19)
}

export function relativeTime(date: string | null | undefined, now: Date, t: Translate): string {
  if (!date) return '-'
  const ms = now.getTime() - new Date(date).getTime()
  if (Number.isNaN(ms)) return '-'
  if (ms < 60_000) return t('common.time.justNow')
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 60) return t('common.time.minutesAgo', { n: minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t('common.time.hoursAgo', { n: hours })
  return t('common.time.daysAgo', { n: Math.floor(hours / 24) })
}
