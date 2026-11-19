import { afterEach, describe, expect, it, vi } from 'vitest'

import { formatShortTime } from '../accountTime'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.documentElement.removeAttribute('lang')
})

// Tip times (12.12-9): digits only, local time, the same in every interface language.
describe('formatShortTime', () => {
  const at = (month: number, day: number, hour: number, minute: number) => new Date(2026, month - 1, day, hour, minute).toISOString()

  it('is HH:MM today and MM-DD HH:MM on other days, padded to two digits', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 15, 9, 30))
    expect(formatShortTime(at(11, 15, 7, 5))).toBe('07:05')
    expect(formatShortTime(at(11, 15, 0, 0))).toBe('00:00')
    expect(formatShortTime(at(11, 14, 7, 43))).toBe('11-14 07:43')
    expect(formatShortTime(new Date(2026, 0, 3, 23, 9).getTime())).toBe('01-03 23:09')
    expect([formatShortTime(null), formatShortTime(''), formatShortTime('not a time')]).toEqual(['—', '—', '—'])
  })

  // Local calendar, not elapsed time: 20 minutes from 23:59 is tomorrow, so it carries the date.
  it('dates a time 20 minutes after a local 23:59', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 18, 23, 59))
    expect(formatShortTime(new Date(Date.now() + 20 * 60_000).toISOString())).toBe('11-19 00:19')
  })

  it('reads the same with an English interface and an English browser locale', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 15, 9, 30))
    document.documentElement.setAttribute('lang', 'en')
    vi.stubGlobal('navigator', { ...navigator, language: 'en-US', languages: ['en-US'] })
    const RealFormat = Intl.DateTimeFormat
    vi.stubGlobal('Intl', { ...Intl, DateTimeFormat: (locale?: string, options?: Intl.DateTimeFormatOptions) => new RealFormat(locale ?? 'en-US', options) })
    expect(formatShortTime(at(11, 14, 7, 43))).toBe('11-14 07:43')
    expect(formatShortTime(at(11, 15, 19, 2))).toBe('19:02')
  })
})
