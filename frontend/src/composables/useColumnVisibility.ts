import { computed, reactive, watch, type Ref } from 'vue'
import { usePhoneViewport } from './useTableViewport'

/** One width's memory of the hidden columns: where it is kept and what it hides until the user changes it. */
export interface ColumnVisibilityTier {
  storageKey: string
  defaultHidden: readonly string[]
  /**
   * Runs on the saved set once, when it is first read (a version chain that hides columns added since): returns true
   * when it changed the set, which is then written back. Not run on the defaults.
   */
  migrate?: (hidden: Set<string>) => boolean
}

export interface UseColumnVisibilityOptions {
  /** The columns that can be shown or hidden (pinned ones left out); `atDefault` compares these. */
  keys: Readonly<Ref<readonly string[]>>
  /** From 768px: the page's existing key, so what a user set before is kept as the wide preference. */
  wide: ColumnVisibilityTier
  /** Below 768px: a key of its own and the phone defaults. */
  narrow: ColumnVisibilityTier
  /**
   * Columns that have just become visible: a toggle, a reset, or a switch to the other width's memory. A column that
   * loads its own data asks for it here.
   */
  onShown?: (keys: string[]) => void
}

type Tier = 'wide' | 'narrow'

const readHidden = (tier: ColumnVisibilityTier): Set<string> => {
  try {
    const raw = localStorage.getItem(tier.storageKey)
    if (raw === null) return new Set(tier.defaultHidden)
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return new Set(tier.defaultHidden)
    const hidden = new Set(parsed.filter((key): key is string => typeof key === 'string'))
    if (tier.migrate?.(hidden)) writeHidden(tier, hidden)
    return hidden
  } catch (error) {
    console.error('Failed to load column settings:', error)
    return new Set(tier.defaultHidden)
  }
}

const writeHidden = (tier: ColumnVisibilityTier, hidden: ReadonlySet<string>) => {
  try {
    localStorage.setItem(tier.storageKey, JSON.stringify([...hidden]))
  } catch (error) {
    console.error('Failed to save column settings:', error)
  }
}

/**
 * Which columns a table shows, remembered twice (mobile list plan, item 16): once below 768px and once from 768px, each
 * with its own defaults and its own "restore defaults". Only the memory in use is read (the other one the first time
 * the width crosses 768px) and only it is written; switching reads, never writes. Column order is not part of it: a
 * page keeps one order for both (useColumnOrder). Keys the page no longer has stay in storage untouched, so columns
 * that come and go (attributes, modes) keep their setting.
 */
export function useColumnVisibility(options: UseColumnVisibilityOptions) {
  const phone = usePhoneViewport()
  const tier = computed<Tier>(() => (phone.value ? 'narrow' : 'wide'))
  const sets = reactive<Partial<Record<Tier, Set<string>>>>({})
  /** A memory is read when its width is first in use; never inside a computed, so reading it changes nothing there. */
  const load = (name: Tier) => {
    if (!sets[name]) sets[name] = readHidden(options[name])
    return sets[name]!
  }
  const hiddenOf = (name: Tier): Set<string> => sets[name] ?? load(name)
  load(tier.value)
  let previous = tier.value
  // Synchronous, so the new memory is there before anything renders with the new width.
  watch(tier, (next) => {
    const before = hiddenOf(previous)
    previous = next
    const after = load(next)
    const shown = [...before].filter((key) => !after.has(key))
    if (shown.length) options.onShown?.(shown)
  }, { flush: 'sync' })

  const hidden = computed<ReadonlySet<string>>(() => sets[tier.value] ?? new Set(options[tier.value].defaultHidden))
  const isVisible = (key: string) => !hidden.value.has(key)

  const toggle = (key: string) => {
    const set = hiddenOf(tier.value)
    const wasHidden = set.delete(key)
    if (!wasHidden) set.add(key)
    writeHidden(options[tier.value], set)
    if (wasHidden) options.onShown?.([key])
  }

  /** The memory in use back to its defaults (its key removed); the other width's memory and the order stay. */
  const reset = () => {
    const name = tier.value
    const set = hiddenOf(name)
    const defaults = new Set(options[name].defaultHidden)
    const shown = [...set].filter((key) => !defaults.has(key))
    sets[name] = defaults
    try {
      localStorage.removeItem(options[name].storageKey)
    } catch (error) {
      console.error('Failed to reset column settings:', error)
    }
    if (shown.length) options.onShown?.(shown)
  }

  const atDefault = computed(() => {
    const defaults = new Set(options[tier.value].defaultHidden)
    return options.keys.value.every((key) => hidden.value.has(key) === defaults.has(key))
  })

  return { narrow: phone, hidden, isVisible, toggle, reset, atDefault }
}
