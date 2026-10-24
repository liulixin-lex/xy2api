import { fieldKind, idOptions, idRef } from './schema'
import type { FieldError, SchemaField } from './types'

type Translate = (key: string, params?: Record<string, unknown>) => string
type Exists = (key: string) => boolean

const P = 'admin.cacheSettings'

/** Translates an enum-like code, falling back to the raw value for codes this build does not know. */
export function codeLabel(group: string, code: string, t: Translate, te: Exists, params?: Record<string, unknown>): string {
  const key = `${P}.${group}.${code.replace(/\+/g, '_plus_')}`
  return te(key) ? t(key, params) : code
}

/** Message parameters for a field error: `limit` from the server, the schema range for what it left out. */
export function errorParams(field: SchemaField | undefined, error: Pick<FieldError, 'limit'>): Record<string, string | number> {
  const min = error.limit?.min ?? field?.min
  const max = error.limit?.max ?? field?.max
  return { min: min ?? '-', max: max ?? '-', limit: max ?? '-' }
}

/** Unit suffix next to a number; `days` is translated, other units are symbols shown as sent. */
export function unitSuffix(unit: string | undefined, t: Translate): string {
  if (!unit) return ''
  if (unit === 'percent') return '%'
  if (unit === 'days') return t(`${P}.units.days`)
  return unit
}

/** "name (#id)" for groups (admin group list) and accounts (the field's own candidates), else the bare ID. */
export function idNames(field: SchemaField | undefined, groups: { id: number; name: string }[], t: Translate) {
  const accounts = field ? idOptions(field) : []
  return {
    group: (id: number) => {
      const group = groups.find((g) => g.id === id)
      return group ? `${group.name} (#${id})` : t(`${P}.groupPicker.groupID`, { id })
    },
    account: (id: number) => {
      const account = accounts.find((a) => a.id === id)
      return account ? `${account.name} (#${id})` : t(`${P}.ids.item`, { id })
    },
  }
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
    return field?.unit ? `${value} ${unitSuffix(field.unit, t)}` : String(value)
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

/** Wall-clock time in the browser's zone, for the on-call admin who is not thinking in UTC (n-7). */
export function formatLocalClock(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

/** "local HH:MM:SS (YYYY-MM-DD HH:MM:SS UTC)" for times an on-call admin acts on (§11.7). */
export function formatBoth(date: string | Date | null | undefined, t: Translate): string {
  if (!date) return '-'
  const d = typeof date === 'string' ? new Date(date) : date
  if (Number.isNaN(d.getTime())) return '-'
  return t(`${P}.timeBoth`, { local: formatLocalClock(d), utc: formatUtc(d) })
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

/** Filled pill and dot classes for an effective state; risk uses an outlined tag instead (m-6). */
export function stateTone(state: string): { pill: string; dot: string } {
  switch (state) {
    case 'on':
      return { pill: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-900/40 dark:text-emerald-100', dot: 'bg-emerald-500' }
    case 'pending_reload':
      return { pill: 'bg-sky-100 text-sky-900 dark:bg-sky-900/40 dark:text-sky-100', dot: 'bg-sky-500' }
    case 'killed':
    case 'parse_error':
    case 'blocked':
      return { pill: 'bg-red-100 text-red-900 dark:bg-red-900/40 dark:text-red-100', dot: 'bg-red-500' }
    case 'masked_by_master':
      return { pill: 'bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100', dot: 'bg-amber-500' }
    default:
      return { pill: 'bg-gray-100 text-gray-800 dark:bg-dark-700 dark:text-dark-100', dot: 'bg-gray-400 dark:bg-dark-400' }
  }
}
