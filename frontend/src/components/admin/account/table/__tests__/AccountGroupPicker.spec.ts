import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import AccountGroupPicker from '../AccountGroupPicker.vue'

const { getById, showError } = vi.hoisted(() => ({ getById: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById } } }))
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

function mountPicker(save = vi.fn().mockResolvedValue(undefined)) {
  const anchor = document.createElement('td')
  document.body.appendChild(anchor)
  const wrapper = mount(AccountGroupPicker, {
    attachTo: document.body,
    props: { account: { id: 7, name: 'acc-7', platform: 'openai' }, groups, anchor, save }
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
      .mockRejectedValueOnce({ status: 409, error: 'mixed_channel_warning', message: 'OpenAI + Gemini' })
      .mockResolvedValueOnce(undefined)
    const { wrapper } = mountPicker(save)
    await flushPromises()
    box(2)!.click()
    await outside()
    await new Promise((resolve) => setTimeout(resolve, 0))
    const confirm = document.querySelector<HTMLElement>('[data-test="group-picker-confirm"]')!
    expect(picker()!.querySelector('[data-test="group-picker-conflict"]')!.textContent).toContain('OpenAI + Gemini')
    expect(document.activeElement).toBe(confirm)
    // Another click outside keeps the question open.
    await outside()
    expect(wrapper.emitted('close')).toBeUndefined()

    confirm.click()
    await flushPromises()
    expect(save).toHaveBeenLastCalledWith([1, 2], true)
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
