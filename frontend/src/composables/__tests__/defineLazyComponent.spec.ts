import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref, watch } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { defineLazyComponent, useLazyMount } from '../useLazyMount'


// 与账号页弹窗同样的写法：只在 show 从 false 变为 true 时加载数据（watch 不带 immediate）
const loadModels = vi.fn()
const ModalLikeAccountTest = defineComponent({
  props: { show: Boolean, accountId: { type: Number, default: 0 } },
  setup(props) {
    const models = ref<string[]>([])
    watch(
      () => props.show,
      async (open) => {
        if (!open) return
        loadModels(props.accountId)
        models.value = [`model-for-${props.accountId}`]
      }
    )
    return () => h('div', { 'data-test': 'models' }, props.show ? models.value.join(',') : '')
  }
})

function hostFor(lazy: ReturnType<typeof defineLazyComponent>) {
  return defineComponent({
    setup() {
      const show = ref(false)
      const accountId = ref(0)
      const mounted = useLazyMount(show)
      const open = (id: number) => {
        accountId.value = id
        show.value = true
      }
      return { show, accountId, mounted, open }
    },
    render() {
      return this.mounted ? h(lazy, { show: this.show, accountId: this.accountId }) : null
    }
  })
}

describe('defineLazyComponent', () => {
  it('runs the open watcher on the very first open', async () => {
    loadModels.mockClear()
    const wrapper = mount(hostFor(defineLazyComponent(async () => ({ default: ModalLikeAccountTest }))))
    ;(wrapper.vm as any).open(7)
    await flushPromises()
    expect(loadModels).toHaveBeenCalledTimes(1)
    expect(loadModels).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-test="models"]').text()).toBe('model-for-7')

    ;(wrapper.vm as any).show = false
    await nextTick()
    ;(wrapper.vm as any).open(8)
    await flushPromises()
    expect(loadModels).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="models"]').text()).toBe('model-for-8')
  })

  it('still opens closed-first when the chunk is already loaded on a later page visit', async () => {
    loadModels.mockClear()
    const lazy = defineLazyComponent(async () => ({ default: ModalLikeAccountTest }))
    const first = mount(hostFor(lazy))
    ;(first.vm as any).open(1)
    await flushPromises()
    first.unmount()

    const second = mount(hostFor(lazy))
    ;(second.vm as any).open(2)
    await nextTick()
    await nextTick()
    expect(loadModels).toHaveBeenLastCalledWith(2)
    expect(second.get('[data-test="models"]').text()).toBe('model-for-2')
  })
})
