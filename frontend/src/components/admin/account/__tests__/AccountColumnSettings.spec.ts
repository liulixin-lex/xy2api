import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountColumnSettings from '../AccountColumnSettings.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

// Stands in for VueDraggable: renders the rows and lets a test drop the first row last,
// the way the real component emits a reordered copy of its list.
const VueDraggableStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `
    <div>
      <slot />
      <button data-test="drag-first-to-end" @click="$emit('update:modelValue', [...modelValue.slice(1), modelValue[0]])">drag</button>
    </div>
  `
}

const columns = [
  { key: 'id', label: 'ID' },
  { key: 'status', label: 'Status' },
  { key: 'last_used_at', label: 'Last used' }
]

const mountSettings = (props: Record<string, unknown> = {}) =>
  mount(AccountColumnSettings, {
    props: { columns, isVisible: (key: string) => key !== 'status', ...props },
    global: { stubs: { VueDraggable: VueDraggableStub, Icon: true } }
  })

const rowLabels = (wrapper: ReturnType<typeof mountSettings>) =>
  wrapper.findAll('[data-test="column-setting"]').map((row) => row.text())

describe('AccountColumnSettings', () => {
  it('lists columns in the given order with a drag handle and the visibility check', () => {
    const wrapper = mountSettings()
    expect(rowLabels(wrapper)).toEqual(['ID', 'Status', 'Last used'])
    expect(wrapper.findAll('.column-drag-handle')).toHaveLength(3)
    expect(wrapper.get('.column-drag-handle').attributes('title')).toBe('admin.accounts.dragToReorderColumns')
    const checks = wrapper.findAll('[data-test="column-setting"]').map((row) => row.find('icon-stub').exists())
    expect(checks).toEqual([true, false, true])
  })

  it('emits the new key order after a drag and shows it right away', async () => {
    const wrapper = mountSettings()
    await wrapper.get('[data-test="drag-first-to-end"]').trigger('click')
    expect(wrapper.emitted('reorder')).toEqual([[['status', 'last_used_at', 'id']]])
    expect(rowLabels(wrapper)).toEqual(['Status', 'Last used', 'ID'])
  })

  it('toggles visibility from the label and only offers reset for a customised order', async () => {
    const wrapper = mountSettings()
    await wrapper.findAll('[data-test="column-setting"] button')[1].trigger('click')
    expect(wrapper.emitted('toggle')).toEqual([['status']])
    expect(wrapper.find('[data-test="reset-column-order"]').exists()).toBe(false)

    await wrapper.setProps({ customized: true })
    await wrapper.get('[data-test="reset-column-order"]').trigger('click')
    expect(wrapper.emitted('reset')).toEqual([[]])
  })

  it('follows a new order from the parent, for example after a reset', async () => {
    const wrapper = mountSettings()
    await wrapper.setProps({ columns: [...columns].reverse() })
    expect(rowLabels(wrapper)).toEqual(['Last used', 'Status', 'ID'])
  })
})
