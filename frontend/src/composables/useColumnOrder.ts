import { computed, ref, type Ref } from 'vue'

/**
 * Merge a saved column order with the current default column keys.
 *
 * Saved keys that no longer exist are dropped and duplicates ignored. Columns the saved
 * order does not know (added in a later release, or only present in some modes) keep their
 * default place: right after the nearest default predecessor that is already placed.
 */
export function mergeColumnOrder(defaultKeys: readonly string[], savedOrder: readonly string[]): string[] {
  const known = new Set(defaultKeys)
  const result: string[] = []
  for (const key of savedOrder) {
    if (known.has(key) && !result.includes(key)) result.push(key)
  }
  defaultKeys.forEach((key, index) => {
    if (result.includes(key)) return
    let insertAt = 0
    for (let i = index - 1; i >= 0; i--) {
      const at = result.indexOf(defaultKeys[i])
      if (at !== -1) {
        insertAt = at + 1
        break
      }
    }
    result.splice(insertAt, 0, key)
  })
  return result
}

const readSavedOrder = (storageKey: string): string[] => {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(storageKey) || '[]')
    return Array.isArray(parsed) ? parsed.filter((key): key is string => typeof key === 'string') : []
  } catch (e) {
    console.error('Failed to load saved column order:', e)
    return []
  }
}

/**
 * User-defined order of a table's reorderable columns, persisted in localStorage.
 * Pinned columns (for example selection, name and actions) are not part of `defaultKeys`.
 */
export function useColumnOrder(storageKey: string, defaultKeys: Readonly<Ref<readonly string[]>>) {
  const savedOrder = ref<string[]>(readSavedOrder(storageKey))
  const orderedKeys = computed(() => mergeColumnOrder(defaultKeys.value, savedOrder.value))
  const isCustomized = computed(() => orderedKeys.value.some((key, index) => key !== defaultKeys.value[index]))

  const setOrder = (keys: readonly string[]) => {
    savedOrder.value = [...keys]
    try {
      localStorage.setItem(storageKey, JSON.stringify(savedOrder.value))
    } catch (e) {
      console.error('Failed to save column order:', e)
    }
  }

  const resetOrder = () => {
    savedOrder.value = []
    try {
      localStorage.removeItem(storageKey)
    } catch (e) {
      console.error('Failed to reset column order:', e)
    }
  }

  return { orderedKeys, isCustomized, setOrder, resetOrder }
}
