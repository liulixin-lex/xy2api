import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { useTweenedNumber } from '../useTweenedNumber'

function harness(start: number) {
  const source = ref(start)
  let shown!: ReturnType<typeof useTweenedNumber>
  const wrapper = mount(defineComponent({
    setup() {
      shown = useTweenedNumber(() => source.value, 100)
      return () => h('span', shown.value.toFixed(2))
    },
  }))
  return { source, shown: () => shown.value, wrapper }
}

describe('useTweenedNumber', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    delete document.documentElement.dataset.motion
  })

  it('shows the first figure after a zero as it is, then glides to the next and lands exactly', async () => {
    let clock = 0
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
    vi.stubGlobal('cancelAnimationFrame', () => {})
    vi.spyOn(performance, 'now').mockImplementation(() => clock)
    const t = harness(0)

    t.source.value = 500
    await nextTick()
    expect(t.shown()).toBe(500)

    t.source.value = 1000
    await nextTick()
    clock = 50
    frames.shift()?.(clock)
    expect(t.shown()).toBeGreaterThan(500)
    expect(t.shown()).toBeLessThan(1000)
    clock = 100
    frames.shift()?.(clock)
    expect(t.shown()).toBe(1000)
  })

  it('keeps the last figure while the source drops to zero (the row is folding away)', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const t = harness(300)
    t.source.value = 0
    await nextTick()
    expect(t.shown()).toBe(300)
    t.source.value = 40
    await nextTick()
    expect(t.shown()).toBe(40)
  })

  it('jumps straight to the value when reduced motion is on', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const t = harness(100)
    t.source.value = 900
    await nextTick()
    expect(t.shown()).toBe(900)
  })
})
