import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import AccountGroupPicker from '../AccountGroupPicker.vue'

const { getById, checkMixedChannelRisk, showError } = vi.hoisted(() => ({ getById: vi.fn(), checkMixedChannelRisk: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById, checkMixedChannelRisk } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('@/stores', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/groupCategories', () => ({
  useGroupCategoriesStore: () => ({ categories: [{ id: 9, name: 'GPT', sort_order: 0, group_ids: [2] }], ensureLoaded: vi.fn() })
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

enableAutoUnmount(afterEach)

const groups = [
  { id: 1, name: 'Alpha', alias: 'Al', platform: 'openai', status: 'active', sort_order: 1 },
  { id: 2, name: '晨星组', platform: 'openai', status: 'active', sort_order: 0 },
  { id: 3, name: 'Claude only', platform: 'anthropic', status: 'active', sort_order: 2 }
] as never[]

const picker = () => document.querySelector<HTMLElement>('[data-group-picker]')
const box = (id: number) => document.querySelector<HTMLInputElement>(`[data-group-picker] input[value="${id}"]`)
const outside = async () => {
  document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
  await flushPromises()
}

function mountPicker(save = vi.fn().mockResolvedValue(undefined), opener: HTMLElement | null = null) {
  const anchor = document.createElement('td')
  document.body.appendChild(anchor)
  const wrapper = mount(AccountGroupPicker, {
    attachTo: document.body,
    props: { account: { id: 7, name: 'acc-7', platform: 'openai' }, groups, anchor, save, opener }
  })
  return { wrapper, save }
}

beforeEach(() => {
  document.body.innerHTML = ''
  getById.mockReset().mockResolvedValue({ id: 7, name: 'acc-7', platform: 'openai', group_ids: [1], groups: [], extra: {} })
  showError.mockReset()
})

describe('AccountGroupPicker', () => {
  it('lists only groups the platform can take, in the groups page order, and saves nothing when closed unchanged', async () => {
    const { wrapper, save } = mountPicker()
    await flushPromises()
    expect(Array.from(document.querySelectorAll('[data-group-picker] .gp-name'), (el) => el.textContent)).toEqual(['晨星组', 'Alpha'])
    expect(box(1)!.checked).toBe(true)
    // Search reads alias and name; the category tab keeps its groups.
    const search = document.querySelector<HTMLInputElement>('[data-test="group-picker-search"]')!
    search.value = 'al'
    search.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(Array.from(document.querySelectorAll('[data-group-picker] .gp-name'), (el) => el.textContent)).toEqual(['Alpha'])

    await outside()
    expect(save).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('saves once on close after ticking, with new groups last', async () => {
    const { wrapper, save } = mountPicker()
    await flushPromises()
    box(2)!.click()
    await flushPromises()
    expect(save).not.toHaveBeenCalled()
    expect(document.querySelector('[data-test="group-picker-foot"]')!.textContent).toContain('"n":2')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(save).toHaveBeenCalledTimes(1)
    expect(save).toHaveBeenCalledWith([1, 2], false)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('confirms a mixed-channel conflict inside the picker with the focus on confirm, and sends it again with the flag', async () => {
    const save = vi.fn()
      .mockRejectedValueOnce({ status: 409, error: 'mixed_channel_warning', message: "mixed_channel_warning: Group '晨星组' contains both openai and gemini accounts." })
      .mockResolvedValueOnce(undefined)
    checkMixedChannelRisk.mockResolvedValueOnce({ has_risk: true, details: { group_id: 2, group_name: '晨星组', current_platform: 'openai', other_platform: 'gemini' } })
    // The account's pills: the picker was opened from the second one.
    const pills = [1, 2].map(() => {
      const pill = document.createElement('button')
      pill.dataset.groupsOpener = '7'
      return document.body.appendChild(pill)
    })
    const { wrapper } = mountPicker(save, pills[1])
    await flushPromises()
    box(2)!.click()
    await outside()
    await new Promise((resolve) => setTimeout(resolve, 0))
    const confirm = document.querySelector<HTMLElement>('[data-test="group-picker-confirm"]')!
    // In the account editor's words, the group and platforms from the check; never the backend's English (7n-4).
    expect(checkMixedChannelRisk).toHaveBeenCalledWith({ platform: 'openai', group_ids: [1, 2], account_id: 7 })
    expect(picker()!.querySelector('[data-test="group-picker-conflict-detail"]')!.textContent).toBe(
      'admin.accounts.mixedChannelWarning:{"groupName":"晨星组","currentPlatform":"openai","otherPlatform":"gemini"}'
    )
    expect(picker()!.querySelector('[data-test="group-picker-conflict"]')!.textContent).not.toContain('contains both')
    expect(document.activeElement).toBe(confirm)
    // Another click outside keeps the question open.
    await outside()
    expect(wrapper.emitted('close')).toBeUndefined()

    confirm.click()
    await flushPromises()
    expect(save).toHaveBeenLastCalledWith([1, 2], true)
    expect(wrapper.emitted('close')).toHaveLength(1)
    // The save took the confirm button away; focus goes back to the pill the picker came from (7i-4).
    expect(document.activeElement).toBe(pills[1])
  })

  it('below 1024px opens under its anchor and right-aligned with it, or above it when there is no room below', async () => {
    const narrow = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query) => ({ matches: false, media: query, onchange: null, addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn() }) as MediaQueryList
    )
    const width = vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(280)
    const height = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(300)
    let rect = { top: 400, bottom: 424, left: 16, right: 374 }
    const anchorRect = vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(() => rect as DOMRect)
    vi.stubGlobal('innerWidth', 390)
    vi.stubGlobal('innerHeight', 844)
    onTestFinished(() => {
      ;[narrow, width, height, anchorRect].forEach((spy) => spy.mockRestore())
      vi.unstubAllGlobals()
    })

    const below = mountPicker()
    await flushPromises()
    expect([picker()!.style.left, picker()!.style.top]).toEqual(['94px', '428px'])
    below.wrapper.unmount()

    rect = { top: 676, bottom: 700, left: 16, right: 374 }
    mountPicker()
    await flushPromises()
    expect([picker()!.style.left, picker()!.style.top]).toEqual(['94px', '372px'])
  })

  it('closes across 1024px (its anchor goes), saving a change on the way (7l-6)', async () => {
    let wide = true
    const listeners: Array<(event: { matches: boolean }) => void> = []
    const media = vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      get matches() { return wide },
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: (event: { matches: boolean }) => void) => listeners.push(listener),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }) as unknown as MediaQueryList)
    onTestFinished(() => media.mockRestore())
    const { wrapper, save } = mountPicker()
    await flushPromises()
    box(2)!.click()
    wide = false
    listeners.forEach((listener) => listener({ matches: false }))
    await flushPromises()
    expect(save).toHaveBeenCalledWith([1, 2], false)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('cancelling the conflict closes without sending again (the failed edit already put the old groups back)', async () => {
    const save = vi.fn().mockRejectedValue({ status: 409, error: 'mixed_channel_warning', message: '' })
    const { wrapper } = mountPicker(save)
    await flushPromises()
    box(2)!.click()
    await outside()
    document.querySelector<HTMLElement>('[data-test="group-picker-cancel"]')!.click()
    await flushPromises()
    expect(save).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
