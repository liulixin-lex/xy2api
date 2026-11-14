import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'

import { useOverlayLayer } from '../useOverlayLayer'
import { closeOverlay, isTopOverlay, openOverlay } from '@/utils/overlayStack'

afterEach(() => {
  document.body.innerHTML = ''
  document.body.className = ''
})

describe('useOverlayLayer', () => {
  it('keeps a layer\'s place in the stack when only its modal flag changes (7p-2)', async () => {
    const app = document.body.appendChild(Object.assign(document.createElement('main'), { id: 'app' }))
    const modal = ref(false)
    const Panel = defineComponent({
      setup() {
        const root = ref<HTMLElement | null>(null)
        useOverlayLayer(() => true, { root, modal })
        return () => h('aside', { ref: root, tabindex: -1 })
      }
    })
    const wrapper = mount(Panel, { attachTo: document.body })
    await nextTick()
    const panel = wrapper.element as HTMLElement

    // A confirm dialog opened above the panel.
    const dialog = Symbol('dialog')
    const dialogRoot = document.body.appendChild(document.createElement('div'))
    openOverlay(dialog, { root: () => dialogRoot, panel: () => dialogRoot })

    modal.value = true
    await nextTick()
    await nextTick()
    expect(isTopOverlay(dialog)).toBe(true)
    expect(panel.inert).toBe(true)
    expect(app.inert).toBe(true)

    modal.value = false
    await nextTick()
    await nextTick()
    expect(isTopOverlay(dialog)).toBe(true)

    closeOverlay(dialog)
    expect(panel.inert).toBe(false)
    wrapper.unmount()
  })
})
