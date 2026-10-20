import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AnimatedNumber from '../AnimatedNumber.vue'

describe('AnimatedNumber', () => {
  let frames: FrameRequestCallback[] = []

  beforeEach(() => {
    frames = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    document.documentElement.dataset.motion = 'full'
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    delete document.documentElement.dataset.motion
  })

  const flushFrame = (now: number) => {
    const pending = frames
    frames = []
    pending.forEach((cb) => cb(now))
  }

  it('counts up from zero on first display and lands on the exact value', async () => {
    const start = performance.now()
    const wrapper = mount(AnimatedNumber, { props: { value: 1200 } })
    expect(wrapper.text()).toBe('0')
    flushFrame(start + 200)
    await nextTick()
    const mid = Number(wrapper.text())
    expect(mid).toBeGreaterThan(0)
    expect(mid).toBeLessThan(1200)
    flushFrame(start + 1000)
    await nextTick()
    expect(wrapper.text()).toBe('1200')
  })

  it('shows later updates immediately without animating', async () => {
    const wrapper = mount(AnimatedNumber, { props: { value: 5 } })
    flushFrame(performance.now() + 1000)
    await wrapper.setProps({ value: 9 })
    expect(wrapper.text()).toBe('9')
    expect(frames).toHaveLength(0)
  })

  it('skips the animation when motion is reduced', () => {
    document.documentElement.dataset.motion = 'reduced'
    const wrapper = mount(AnimatedNumber, { props: { value: 42, format: (v: number) => `+${v}` } })
    expect(wrapper.text()).toBe('+42')
    expect(frames).toHaveLength(0)
  })
})
