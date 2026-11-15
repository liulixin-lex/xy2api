import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import AccountGroupPills from '../AccountGroupPills.vue'
import { formatShortTime } from '../accountTime'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'
import type { AdminGroup } from '@/types'
import { closeOverlay, openOverlay } from '@/utils/overlayStack'
import type { TipContent } from '@/utils/tip'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true })
}))

type Quality = Pick<BackendAccountSummary, 'status' | 'stop' | 'substandard' | 'exemptions' | 'ttfo'>
const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai', status: 'active' }) as AdminGroup
const baseQuality = (patch: Partial<Quality> = {}): Quality => ({
  status: 'normal', stop: null, substandard: [], exemptions: [], ttfo: { n: 30, short: false, red: false, p50_ms: 2800 }, ...patch
})
const quality = baseQuality()
const mountPills = (count: number, withQuality: Quality | boolean = true) => mount(AccountGroupPills, {
  attachTo: document.body,
  props: {
    account: { id: 7, name: 'acc-7' } as never,
    groups: Array.from({ length: count }, (_, i) => group(i + 1)),
    quality: withQuality === true ? quality : withQuality || null
  }
})
const back = (groupId: number, model: string, dims: Quality['substandard'][number]['dims'], until: string) =>
  ({ group_id: groupId, model, dims, until, other_group: false, exempt: false, effective: true })
const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString()
/** The card the zone's tip shows (its v-tip value, called as the tip does when it shows). */
const cardOf = (el: { element: Element }) => {
  const value = (el.element as HTMLElement & { __tipText?: TipContent | (() => TipContent) }).__tipText
  return typeof value === 'function' ? value() : value
}
const backIn = expect.stringMatching(/^admin\.accounts\.table\.pillBackUntil:/)
const layer = { modal: false, root: () => null, panel: () => null }
const tipShown = () => document.getElementById('sq-tip')?.hidden === false
/** Leaves whatever was hovered, hovers the zone for the tip's delay (fake timers) and says whether a tip shows. */
let hovered: Element | null = null
function hoverShows(el: Element) {
  hovered?.dispatchEvent(new Event('mouseleave'))
  el.dispatchEvent(new Event('mouseenter'))
  hovered = el
  vi.advanceTimersByTime(400)
  return tipShown()
}

// The pointer type is one shared media query: it is read again once no pill uses it.
enableAutoUnmount(afterEach)
afterEach(() => {
  hovered = null
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

  it('puts a pill\'s card away on the click that opens a layer, and keeps it away while that layer is open (7i-3)', async () => {
    vi.useFakeTimers()
    const wrapper = mountPills(1)
    const dot = wrapper.get('[data-test="group-pill-dot"]').element
    hoverShows(dot)
    expect(tipShown()).toBe(true)
    await wrapper.get('[data-test="group-pill-dot"]').trigger('click')
    expect(tipShown()).toBe(false)
    // The panel it opened is open: no card, so Escape reaches the panel.
    const panel = Symbol('panel')
    openOverlay(panel, layer)
    hoverShows(dot)
    expect(tipShown()).toBe(false)
    closeOverlay(panel)
    hoverShows(dot)
    expect(tipShown()).toBe(true)
    vi.useRealTimers()
  })

  // Both zones show the same card, so the quiet is the pill's, not the zone's (12.12-8).
  it('opened from the dot: hovering the same pill\'s text shows no card, other pills and "+N" show theirs', async () => {
    vi.useFakeTimers()
    onTestFinished(() => vi.useRealTimers())
    const wrapper = mountPills(10)
    const [first, second] = wrapper.findAll('[data-test="group-pill"]')
    await first.get('[data-test="group-pill-dot"]').trigger('click')
    const panel = Symbol('panel')
    openOverlay(panel, layer)
    expect(hoverShows(first.get('[data-test="group-pill-text"]').element)).toBe(false)
    expect(hoverShows(first.get('[data-test="group-pill-dot"]').element)).toBe(false)
    expect(hoverShows(second.get('[data-test="group-pill-text"]').element)).toBe(true)
    expect(hoverShows(wrapper.get('[data-test="group-pill-more-text"]').element)).toBe(true)
    closeOverlay(panel)
    expect(hoverShows(first.get('[data-test="group-pill-text"]').element)).toBe(true)

    // "+N" alike: opened from its dot, its text shows no card.
    await wrapper.get('[data-test="group-pill-more"] .pill__dot').trigger('click')
    openOverlay(panel, layer)
    expect(hoverShows(wrapper.get('[data-test="group-pill-more-text"]').element)).toBe(false)
    expect(hoverShows(first.get('[data-test="group-pill-text"]').element)).toBe(true)
    closeOverlay(panel)
  })

  it('opened from the text: hovering the same pill\'s dot shows no card until the picker closes', async () => {
    vi.useFakeTimers()
    onTestFinished(() => vi.useRealTimers())
    const wrapper = mountPills(2)
    const [first, second] = wrapper.findAll('[data-test="group-pill"]')
    await first.get('[data-test="group-pill-text"]').trigger('click')
    const picker = Symbol('picker')
    openOverlay(picker, layer)
    expect(hoverShows(first.get('[data-test="group-pill-dot"]').element)).toBe(false)
    expect(hoverShows(second.get('[data-test="group-pill-dot"]').element)).toBe(true)
    closeOverlay(picker)
    expect(hoverShows(first.get('[data-test="group-pill-dot"]').element)).toBe(true)
  })

  it('is quiet only until its own layer closes; over any layer Escape closes the tip and goes on to the layer (7m-2)', async () => {
    vi.useFakeTimers()
    const wrapper = mountPills(1)
    await wrapper.get('[data-test="group-pill-dot"]').trigger('click')
    const panel = Symbol('panel')
    openOverlay(panel, layer)
    closeOverlay(panel)
    const other = Symbol('other')
    openOverlay(other, layer)
    expect(hoverShows(wrapper.get('[data-test="group-pill-text"]').element)).toBe(true)

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
    expect(Array.from(tip.querySelectorAll('.sq-tip__c.sq-tip__first'), (el) => el.textContent)).toEqual(['Group 9', 'Group 10', 'Group 11', 'Group 12'])
  })

  // Tip cards (12.7): data and state words only, the same card on both zones.
  it('shows the group\'s name and state on both zones; at the back a row per model with why and when it is back', () => {
    const wrapper = mountPills(2, baseQuality({
      substandard: [back(2, 'gpt-6-astra', ['ttfo', 'error'], inMinutes(15)), back(2, 'gpt-5', ['cache'], inMinutes(-1))]
    }))
    const [ok, atBack] = wrapper.findAll('[data-test="group-pill"]')
    expect(cardOf(ok.get('[data-test="group-pill-dot"]'))).toEqual({
      title: 'Group 1', chip: { text: 'admin.accounts.table.dotStates.ok', tone: 'ok', dot: true }, rows: []
    })
    const card = cardOf(atBack.get('[data-test="group-pill-dot"]'))
    expect(cardOf(atBack.get('[data-test="group-pill-text"]'))).toEqual(card)
    expect(card).toEqual({
      title: 'Group 2',
      chip: { text: 'admin.accounts.table.dotStates.back', tone: 'back', dot: true },
      rows: [
        ['Astra', { text: 'admin.accounts.table.dimReasons.ttfo、admin.accounts.table.dimReasons.error', tone: 'back' }, backIn],
        ['gpt-5', { text: 'admin.accounts.table.dimReasons.cache', tone: 'back' }, '']
      ]
    })
  })

  it('shows an exemption\'s end and reason, the TTFT samples while observed, and a stop\'s reason, source and end', () => {
    const exempt = mountPills(1, baseQuality({ exemptions: [{ group_id: 0, expires_at: null, reason: '试用' }] }))
    expect(cardOf(exempt.get('[data-test="group-pill-dot"]'))).toMatchObject({
      chip: { text: 'admin.accounts.table.dotStates.exempt', tone: 'exempt', dot: true },
      rows: [['admin.accounts.table.tip.exemptUntil', 'admin.accounts.table.tip.forever'], ['admin.accounts.table.tip.reason', '试用']]
    })
    const observed = mountPills(1, baseQuality({ status: 'observing', ttfo: { n: 12, short: true, red: false, p50_ms: null } }))
    expect(cardOf(observed.get('[data-test="group-pill-dot"]'))).toMatchObject({
      chip: { tone: 'observe' }, rows: [['admin.accounts.table.tip.samples', 'admin.accounts.table.tip.count:{"n":12}']]
    })
    const until = inMinutes(90)
    const stopped = mountPills(1, baseQuality({ stop: { source: 'manual', reason: 'manual_off', until } }))
    expect(cardOf(stopped.get('[data-test="group-pill-dot"]'))).toMatchObject({
      chip: { text: 'admin.accounts.table.dotStates.stopped', tone: 'stopped', dot: true },
      rows: [
        ['admin.accounts.table.tip.reason', 'admin.schedQuality.kind.manual_off'],
        ['admin.accounts.table.tip.source', 'admin.accounts.table.tip.sources.manual'],
        ['admin.accounts.table.tip.resume', formatShortTime(until)]
      ]
    })
  })

  it('lists each folded group under "+N" with its dot, state and end; without quality data names only; "+" has no tip', () => {
    const expires = inMinutes(120)
    const wrapper = mountPills(11, baseQuality({ substandard: [back(10, 'gpt-6.1-sol', ['error'], inMinutes(5))], exemptions: [{ group_id: 11, expires_at: expires, reason: '' }] }))
    expect(cardOf(wrapper.get('[data-test="group-pill-more-text"]'))).toEqual({
      title: 'admin.accounts.table.pillMore:{"n":3}',
      rows: [
        ['Group 9', { text: 'admin.accounts.table.dotStates.ok', tone: 'ok', dot: true }, ''],
        ['Group 10', { text: 'admin.accounts.table.dotStates.back', tone: 'back', dot: true }, backIn],
        ['Group 11', { text: 'admin.accounts.table.dotStates.exempt', tone: 'exempt', dot: true }, formatShortTime(expires)]
      ]
    })
    const plain = mountPills(10, false)
    expect(cardOf(plain.get('[data-test="group-pill-text"]'))).toEqual({ title: 'Group 1', chip: { text: 'admin.accounts.table.pillNoQuality', tone: 'muted' } })
    expect(cardOf(plain.get('[data-test="group-pill-more-text"]'))).toEqual({
      title: 'admin.accounts.table.pillMore:{"n":2}', chip: { text: 'admin.accounts.table.pillNoQuality', tone: 'muted' }, rows: [['Group 9'], ['Group 10']]
    })
    expect(cardOf(mountPills(0).get('[data-test="group-pill-empty"]'))).toBeUndefined()
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
    expect(document.querySelector('#sq-tip .sq-tip__title')!.textContent).toBe('Group 1')
    expect(document.querySelector('#sq-tip .sq-tip__chip')!.textContent).toBe('admin.accounts.table.pillNoQuality')
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
