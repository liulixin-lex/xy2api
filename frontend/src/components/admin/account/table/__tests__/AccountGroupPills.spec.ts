import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import AccountGroupPills from '../AccountGroupPills.vue'
import type { AdminGroup } from '@/types'
import { closeOverlay, openOverlay } from '@/utils/overlayStack'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai', status: 'active' }) as AdminGroup
const quality = { account_id: 7, status: 'normal', reset_at: null, stop: null, substandard: [], exemptions: [] } as never
const mountPills = (count: number, withQuality = true) => mount(AccountGroupPills, {
  attachTo: document.body,
  props: { account: { id: 7, name: 'acc-7' } as never, groups: Array.from({ length: count }, (_, i) => group(i + 1)), quality: withQuality ? quality : null }
})

// The pointer type is one shared media query: it is read again once no pill uses it.
enableAutoUnmount(afterEach)
afterEach(() => {
  document.body.innerHTML = ''
})

// Groups column (7d-12): each pill has two hit zones.
describe('AccountGroupPills', () => {
  it('opens the quality panel on the dot and edits the groups on the text', async () => {
    const wrapper = mountPills(2)
    const [first, second] = wrapper.findAll('[data-test="group-pill"]')
    await second.get('[data-test="group-pill-dot"]').trigger('click')
    expect(wrapper.emitted('open-quality')).toEqual([[2]])
    expect(wrapper.emitted('edit-groups')).toBeUndefined()
    await first.get('[data-test="group-pill-text"]').trigger('click')
    expect(wrapper.emitted('edit-groups')).toHaveLength(1)
    expect(wrapper.emitted('open-quality')).toHaveLength(1)
  })

  it('puts a zone\'s tip away on the click that opens a layer, and keeps it away while that layer is open (7i-3)', async () => {
    vi.useFakeTimers()
    const wrapper = mountPills(1)
    const dot = wrapper.get('[data-test="group-pill-dot"]').element
    const tipShown = () => document.getElementById('sq-tip')?.hidden === false
    const hover = () => {
      dot.dispatchEvent(new Event('mouseleave'))
      dot.dispatchEvent(new Event('mouseenter'))
      vi.advanceTimersByTime(400)
    }
    hover()
    expect(tipShown()).toBe(true)
    await wrapper.get('[data-test="group-pill-dot"]').trigger('click')
    expect(tipShown()).toBe(false)
    // The panel it opened is open: no tip, so Escape reaches the panel.
    const panel = Symbol('panel')
    openOverlay(panel, { modal: false, root: () => null, panel: () => null })
    hover()
    expect(tipShown()).toBe(false)
    closeOverlay(panel)
    hover()
    expect(tipShown()).toBe(true)
    vi.useRealTimers()
  })

  it('is quiet only until its own layer closes; over any layer Escape closes the tip and goes on to the layer (7m-2)', async () => {
    vi.useFakeTimers()
    const wrapper = mountPills(1)
    const dot = wrapper.get('[data-test="group-pill-dot"]').element
    const tipShown = () => document.getElementById('sq-tip')?.hidden === false
    const hover = () => {
      dot.dispatchEvent(new Event('mouseleave'))
      dot.dispatchEvent(new Event('mouseenter'))
      vi.advanceTimersByTime(400)
    }
    const layer = { modal: false, root: () => null, panel: () => null }
    await wrapper.get('[data-test="group-pill-dot"]').trigger('click')
    const panel = Symbol('panel')
    openOverlay(panel, layer)
    closeOverlay(panel)
    const other = Symbol('other')
    openOverlay(other, layer)
    hover()
    expect(tipShown()).toBe(true)

    const reached = vi.fn()
    document.body.addEventListener('keydown', reached)
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(tipShown()).toBe(false)
    expect(reached).toHaveBeenCalledTimes(1)
    document.body.removeEventListener('keydown', reached)
    closeOverlay(other)
    vi.useRealTimers()
  })

  it('has no dot without quality data: the whole pill edits', () => {
    const wrapper = mountPills(1, false)
    expect(wrapper.find('[data-test="group-pill-dot"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="group-pill"]').classes()).toContain('pill--nodot')
  })

  it('lists the folded groups when "+N" is clicked, without opening the editor (7d-7)', async () => {
    const wrapper = mountPills(12)
    const more = wrapper.get('[data-test="group-pill-more-text"]')
    expect(more.text()).toBe('+4')
    await more.trigger('click')
    expect(wrapper.emitted('edit-groups')).toBeUndefined()
    const tip = document.getElementById('sq-tip')!
    expect(tip.hidden).toBe(false)
    expect(tip.textContent!.split('\n').slice(1).map((line) => line.split(' · ')[0])).toEqual(
      [9, 10, 11, 12].map((id) => expect.stringContaining(`Group ${id}`))
    )
  })

  it('on a touch screen opens the quality panel from the whole pill, at any width; a pill with no dot shows its full name (spec 4.3, 7l-5)', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
    const wrapper = mountPills(12)
    expect(wrapper.find('[data-test="group-pill-dot"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="group-pill-text"]').exists()).toBe(false)
    await wrapper.findAll('[data-test="group-pill"]')[1].trigger('click')
    await wrapper.get('[data-test="group-pill-more"]').trigger('click')
    expect(wrapper.emitted('open-quality')).toEqual([[2], [9]])
    expect(wrapper.emitted('edit-groups')).toBeUndefined()
    wrapper.unmount()

    const plain = mountPills(1, false)
    await plain.get('[data-test="group-pill"]').trigger('click')
    expect(plain.emitted('open-quality')).toBeUndefined()
    expect(document.getElementById('sq-tip')!.textContent).toBe('admin.accounts.table.pillNoQuality:{"name":"Group 1"}')
    plain.unmount()
    window.matchMedia = matchMedia
  })

  it('keeps a tip a tap brought up through the mouse events after the tap, until a press elsewhere or 4s (7q-6)', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
    vi.useFakeTimers()
    onTestFinished(() => {
      vi.useRealTimers()
      window.matchMedia = matchMedia
    })
    const wrapper = mountPills(1, false)
    const pill = wrapper.get('[data-test="group-pill"]')
    const tap = async (pointerType: string) => {
      pill.element.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType }))
      await pill.trigger('click')
    }

    await tap('touch')
    await pill.trigger('mouseleave')
    expect(document.getElementById('sq-tip')!.hidden).toBe(false)
    vi.advanceTimersByTime(4000)
    expect(document.getElementById('sq-tip')!.hidden).toBe(true)

    await tap('touch')
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'touch' }))
    expect(document.getElementById('sq-tip')!.hidden).toBe(true)

    // A mouse click's tip goes on mouseleave, as a hover tip does.
    await tap('mouse')
    await pill.trigger('mouseleave')
    expect(document.getElementById('sq-tip')!.hidden).toBe(true)
  })
})
