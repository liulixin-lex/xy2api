import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import { useLazyMount } from '../useLazyMount'

describe('useLazyMount', () => {
  it('stays false until the source first becomes truthy, then stays true', async () => {
    const show = ref(false)
    const mounted = useLazyMount(show)
    expect(mounted.value).toBe(false)

    show.value = true
    // flush: 'sync' — 与 show 同一 tick 生效，弹窗首次打开不会晚一帧
    expect(mounted.value).toBe(true)

    show.value = false
    await nextTick()
    expect(mounted.value).toBe(true)
  })

  it('is true immediately when the source starts truthy', () => {
    expect(useLazyMount(() => 'open').value).toBe(true)
  })
})
