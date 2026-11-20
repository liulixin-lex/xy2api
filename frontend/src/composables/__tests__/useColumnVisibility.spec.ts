import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'

import { useColumnVisibility } from '../useColumnVisibility'

// Two memories of the shown columns (mobile list plan, item 16): below 768px and from 768px, each with its own defaults
// and reset; the wide one keeps the page's old key, so a user's settings carry over.

let wide = true
const listeners = new Set<(event: { matches: boolean }) => void>()
const setWidth = (width: number) => {
  wide = width >= 768
  listeners.forEach((listener) => listener({ matches: wide }))
}
const matchMedia = window.matchMedia
beforeEach(() => {
  localStorage.clear()
  listeners.clear()
  wide = true
  window.matchMedia = ((query: string) => ({
    get matches() { return query === '(min-width: 768px)' ? wide : true },
    media: query,
    addEventListener: (_: string, listener: (event: { matches: boolean }) => void) => listeners.add(listener),
    removeEventListener: (_: string, listener: (event: { matches: boolean }) => void) => listeners.delete(listener),
    addListener: vi.fn(),
    removeListener: vi.fn()
  })) as unknown as typeof window.matchMedia
})
afterEach(() => {
  window.matchMedia = matchMedia
})

const KEYS = ['status', 'groups', 'iq_check', 'capacity', 'notes']
const setup = (extra: Partial<Parameters<typeof useColumnVisibility>[0]> = {}) => {
  const scope = effectScope()
  const shown: string[][] = []
  const columns = scope.run(() => useColumnVisibility({
    keys: ref(KEYS),
    wide: { storageKey: 'page-hidden-columns', defaultHidden: ['notes'] },
    narrow: { storageKey: 'page-hidden-columns-phone', defaultHidden: ['notes', 'iq_check', 'capacity'] },
    onShown: (keys) => shown.push(keys),
    ...extra
  }))!
  return { columns, shown, stop: () => scope.stop() }
}
const stored = (key: string) => JSON.parse(localStorage.getItem(key) ?? 'null')

describe('useColumnVisibility', () => {
  it('keeps the old key as the wide memory: a saved setting carries over from 768px', () => {
    localStorage.setItem('page-hidden-columns', JSON.stringify(['groups']))
    const { columns, stop } = setup()
    expect(columns.narrow.value).toBe(false)
    expect(KEYS.filter(columns.isVisible)).toEqual(['status', 'iq_check', 'capacity', 'notes'])
    expect(columns.atDefault.value).toBe(false)
    stop()
  })

  it('starts a phone on its own defaults and never writes the other width while switching', async () => {
    localStorage.setItem('page-hidden-columns', JSON.stringify(['groups']))
    setWidth(390)
    const { columns, stop } = setup()
    expect(columns.narrow.value).toBe(true)
    expect(KEYS.filter(columns.isVisible)).toEqual(['status', 'groups'])
    expect(columns.atDefault.value).toBe(true)
    setWidth(1280)
    await nextTick()
    setWidth(390)
    await nextTick()
    expect(stored('page-hidden-columns')).toEqual(['groups'])
    expect(localStorage.getItem('page-hidden-columns-phone')).toBeNull()
    stop()
  })

  it('toggles and resets the memory in use only', async () => {
    const { columns, shown, stop } = setup()
    columns.toggle('notes')
    expect(stored('page-hidden-columns')).toEqual([])
    expect(shown.at(-1)).toEqual(['notes'])

    setWidth(390)
    await nextTick()
    expect(columns.isVisible('notes')).toBe(false)
    columns.toggle('capacity')
    columns.toggle('status')
    expect(stored('page-hidden-columns-phone')).toEqual(['notes', 'iq_check', 'status'])
    expect(stored('page-hidden-columns')).toEqual([])

    columns.reset()
    expect(localStorage.getItem('page-hidden-columns-phone')).toBeNull()
    expect(KEYS.filter(columns.isVisible)).toEqual(['status', 'groups'])
    expect(stored('page-hidden-columns')).toEqual([])

    setWidth(1280)
    await nextTick()
    expect(columns.isVisible('notes')).toBe(true)
    stop()
  })

  it('reports the columns a switch of width shows, so their data can load', async () => {
    const { shown, stop } = setup()
    setWidth(390)
    await nextTick()
    expect(shown).toEqual([])
    setWidth(1024)
    await nextTick()
    expect(shown).toEqual([['iq_check', 'capacity']])
    stop()
  })

  it('runs a version chain on a saved set once and writes it back; keeps keys the page does not have now', () => {
    localStorage.setItem('page-hidden-columns', JSON.stringify(['attr_9']))
    const migrate = vi.fn((hidden: Set<string>) => {
      if (hidden.has('capacity')) return false
      hidden.add('capacity')
      return true
    })
    const { columns, stop } = setup({ wide: { storageKey: 'page-hidden-columns', defaultHidden: ['notes'], migrate } })
    expect(columns.isVisible('capacity')).toBe(false)
    expect(stored('page-hidden-columns')).toEqual(['attr_9', 'capacity'])
    expect(migrate).toHaveBeenCalledTimes(1)
    stop()
  })
})
