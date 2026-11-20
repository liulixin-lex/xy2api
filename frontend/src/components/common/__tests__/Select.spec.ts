import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import Select from '../Select.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const originalInnerWidth = window.innerWidth
let unmountWrapper: (() => void) | undefined

const setViewportWidth = (width: number) => {
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    value: width,
  })
}

const mockTriggerRect = (left: number, width: number) => {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: left,
    y: 20,
    top: 20,
    right: left + width,
    bottom: 60,
    left,
    width,
    height: 40,
    toJSON: () => ({}),
  })
}

const openSelect = async (props: Record<string, unknown> = {}) => {
  const wrapper = mount(Select, {
    props: {
      modelValue: null,
      options: [
        {
          value: 'example',
          label: 'very-long-unbroken-option-value-that-must-not-overflow',
        },
      ],
      ...props,
    },
  })
  unmountWrapper = () => wrapper.unmount()

  await wrapper.get('button').trigger('click')
  await nextTick()

  return document.body.querySelector<HTMLElement>('.select-dropdown-portal')
}

afterEach(() => {
  unmountWrapper?.()
  unmountWrapper = undefined
  document.body.innerHTML = ''
  setViewportWidth(originalInnerWidth)
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('Select dropdown viewport constraints', () => {
  it('preserves the existing 200px minimum width when space is available', async () => {
    setViewportWidth(1024)
    mockTriggerRect(20, 80)

    const dropdown = await openSelect()

    expect(dropdown).not.toBeNull()
    expect(dropdown?.style.left).toBe('20px')
    expect(dropdown?.style.minWidth).toBe('200px')
    expect(dropdown?.style.maxWidth).toBe('996px')
  })

  it('shrinks the minimum width to fit near the right viewport edge', async () => {
    setViewportWidth(320)
    mockTriggerRect(220, 80)

    const dropdown = await openSelect()

    expect(dropdown).not.toBeNull()
    expect(dropdown?.style.left).toBe('220px')
    expect(dropdown?.style.minWidth).toBe('92px')
    expect(dropdown?.style.maxWidth).toBe('92px')
  })

  it('clamps a trigger left of the viewport to the safe padding', async () => {
    setViewportWidth(320)
    mockTriggerRect(-20, 80)

    const dropdown = await openSelect()

    expect(dropdown).not.toBeNull()
    expect(dropdown?.style.left).toBe('8px')
    expect(dropdown?.style.minWidth).toBe('200px')
    expect(dropdown?.style.maxWidth).toBe('304px')
  })

  it('clamps an offscreen-right trigger position to the viewport boundary', async () => {
    setViewportWidth(320)
    mockTriggerRect(400, 80)

    const dropdown = await openSelect()

    expect(dropdown).not.toBeNull()
    expect(dropdown?.style.left).toBe('312px')
    expect(dropdown?.style.minWidth).toBe('0px')
    expect(dropdown?.style.maxWidth).toBe('0px')
  })

  it('keeps content-sized width unless matchTriggerWidth is set', async () => {
    setViewportWidth(1280)
    mockTriggerRect(100, 848)

    expect((await openSelect())?.style.width).toBe('')
    unmountWrapper?.()
    document.body.innerHTML = ''

    const dropdown = await openSelect({ matchTriggerWidth: true })
    expect(dropdown?.style.width).toBe('848px')
    expect(dropdown?.style.maxWidth).toBe('1172px')
  })

  it('caps a trigger-width dropdown at the viewport edge', async () => {
    setViewportWidth(600)
    mockTriggerRect(100, 848)

    const dropdown = await openSelect({ matchTriggerWidth: true })
    expect(dropdown?.style.width).toBe('492px')
  })
})

describe('Select size sm (12.6)', () => {
  it('marks only the sm trigger, filled once a value is chosen; md keeps its classes', async () => {
    const options = [{ value: '', label: 'All' }, { value: 'a', label: 'A' }]
    const md = mount(Select, { props: { modelValue: 'a', options } })
    expect(md.get('button').classes()).toEqual(['select-trigger'])
    md.unmount()

    const sm = mount(Select, { props: { modelValue: '', options, size: 'sm' } })
    expect(sm.get('button').classes()).toEqual(['select-trigger', 'select-trigger--sm'])
    await sm.setProps({ modelValue: 'a' })
    expect(sm.get('button').classes()).toEqual(['select-trigger', 'select-trigger--sm', 'select-trigger--filled'])
    sm.unmount()
  })

  it('opens a panel at least as wide as the trigger and 168px, at most 320px', async () => {
    setViewportWidth(1280)
    mockTriggerRect(100, 96)
    const narrow = await openSelect({ size: 'sm' })
    expect(narrow?.classList.contains('select-dropdown-portal--sm')).toBe(true)
    expect(narrow?.style.minWidth).toBe('168px')
    expect(narrow?.style.maxWidth).toBe('320px')
    unmountWrapper?.()
    document.body.innerHTML = ''
    vi.restoreAllMocks()

    mockTriggerRect(100, 220)
    expect((await openSelect({ size: 'sm' }))?.style.minWidth).toBe('220px')
  })
})

describe('Select remote search', () => {
  const mountRemoteSelect = (props: Record<string, unknown> = {}) => {
    const wrapper = mount(Select, {
      props: {
        modelValue: null,
        remote: true,
        options: [
          { value: 'alpha', label: 'Alpha account' },
          { value: 'beta', label: 'Beta account' },
        ],
        ...props,
      },
    })
    unmountWrapper = () => wrapper.unmount()
    return wrapper
  }

  const openDropdown = async () => {
    const dropdown = document.body.querySelector<HTMLElement>('.select-dropdown-portal')
    expect(dropdown).not.toBeNull()
    return dropdown as HTMLElement
  }

  const typeSearchQuery = async (query: string) => {
    const dropdown = await openDropdown()
    const input = dropdown.querySelector<HTMLInputElement>('.select-search-input')
    expect(input).not.toBeNull()
    input!.value = query
    input!.dispatchEvent(new Event('input'))
    await nextTick()
  }

  it('emits debounced search events and skips local filtering in remote mode', async () => {
    vi.useFakeTimers()
    const wrapper = mountRemoteSelect()
    await wrapper.get('button').trigger('click')
    await nextTick()

    await typeSearchQuery('zzz')

    // 防抖窗口内不触发。
    expect(wrapper.emitted('search')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(300)

    expect(wrapper.emitted('search')).toEqual([['zzz']])
    // 远程模式不做本地过滤：无命中的 query 下选项仍完整展示（由父组件更新 options）。
    const dropdown = await openDropdown()
    const labels = [...dropdown.querySelectorAll('.select-option-label')].map((el) => el.textContent)
    expect(labels).toContain('Alpha account')
    expect(labels).toContain('Beta account')
  })

  it('does not emit search when the dropdown closes and the query resets', async () => {
    vi.useFakeTimers()
    const wrapper = mountRemoteSelect()
    await wrapper.get('button').trigger('click')
    await nextTick()

    await typeSearchQuery('hidden')

    // 关闭下拉：排队中的防抖定时器应被取消，也不应因 query 重置而尾随 emit。
    await wrapper.get('button').trigger('click')
    await nextTick()
    await vi.advanceTimersByTimeAsync(300)

    expect(wrapper.emitted('search')).toBeUndefined()
  })

  it('shows the loading text instead of empty text while loading with no options', async () => {
    const wrapper = mountRemoteSelect({ options: [], loading: true })
    await wrapper.get('button').trigger('click')
    await nextTick()

    const dropdown = await openDropdown()
    expect(dropdown.querySelector('.select-empty')?.textContent).toContain('common.loading')
  })

  it('keeps local filtering and emits nothing when remote is not set', async () => {
    vi.useFakeTimers()
    const wrapper = mount(Select, {
      props: {
        modelValue: null,
        searchable: true,
        options: [
          { value: 'alpha', label: 'Alpha account' },
          { value: 'beta', label: 'Beta account' },
        ],
      },
    })
    unmountWrapper = () => wrapper.unmount()
    await wrapper.get('button').trigger('click')
    await nextTick()

    await typeSearchQuery('alpha')
    await vi.advanceTimersByTimeAsync(300)

    expect(wrapper.emitted('search')).toBeUndefined()
    const dropdown = await openDropdown()
    const labels = [...dropdown.querySelectorAll('.select-option-label')].map((el) => el.textContent)
    expect(labels).toEqual(['Alpha account'])
  })
})

describe('Select creatable entry', () => {
  it('offers the typed value unless an option already has exactly that value', async () => {
    const dropdown = await openSelect({ searchable: true, creatable: true, creatablePrefix: 'Use', options: [{ value: 'gpt-6.1-sol', label: 'gpt-6.1-sol' }] })
    const input = dropdown!.querySelector<HTMLInputElement>('input')!
    const options = () => Array.from(dropdown!.querySelectorAll('[role="option"]'), (el) => el.textContent?.trim())
    input.value = 'gpt-6.1'
    input.dispatchEvent(new Event('input'))
    await nextTick()
    expect(options()).toEqual(['Use "gpt-6.1"', 'gpt-6.1-sol'])
    input.value = 'gpt-6.1-sol'
    input.dispatchEvent(new Event('input'))
    await nextTick()
    expect(options()).toEqual(['gpt-6.1-sol'])
  })
})
