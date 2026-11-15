import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'

import { useHeldResult } from '../useHeldResult'

describe('useHeldResult', () => {
  it('holds an answer until the snapshot it stood in for changes', async () => {
    const snapshot = ref<{ balance: number }>({ balance: 1 })
    const held = useHeldResult<number | null>(() => snapshot.value)
    expect(held.current.value).toBeNull()

    held.hold(null) // "unknown" on purpose
    expect(held.current.value).toEqual({ value: null })

    snapshot.value = { balance: 1 } // a new object with the same data
    await nextTick()
    expect(held.current.value).toEqual({ value: null })

    snapshot.value = { balance: 2 }
    await nextTick()
    expect(held.current.value).toBeNull()
  })

  it('keeps the answer while asked to, even across snapshot changes', async () => {
    const snapshot = ref(1)
    const open = ref(true)
    const held = useHeldResult<string>(() => snapshot.value, { keep: () => open.value })
    held.hold('fresh')
    snapshot.value = 2
    await nextTick()
    expect(held.current.value).toEqual({ value: 'fresh' })
    held.clear()
    expect(held.current.value).toBeNull()
  })
})
