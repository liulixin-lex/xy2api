import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import IsolatedSvg from '../IsolatedSvg.vue'

enableAutoUnmount(afterEach)

// The custom menu icon configured on the live site: inlined into the page, its <style> recoloured every SVG.
const LIVE_ICON = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor">' +
  '<style>svg { color: #526071; } @media (prefers-color-scheme: dark) { svg { color: #d8e0ea; } }</style>' +
  '<path d="M12 6v12m6-6H6"/></svg>'
// An exported line icon coloured only through a class (the review's example).
const CLASS_ICON = '<svg viewBox="0 0 24 24"><style>.cls-1 { fill: none; stroke: #123456; stroke-width: 2; }</style>' +
  '<path class="cls-1" d="M4 12h16"/></svg>'

function page(svg: string) {
  const wrapper = mount({
    components: { IsolatedSvg },
    template: `<div style="color: rgb(1, 2, 3)"><IsolatedSvg :svg="svg" /></div>`,
    data: () => ({ svg })
  }, { attachTo: document.body })
  const host = wrapper.get('[aria-hidden="true"]').element
  return { wrapper, root: host.shadowRoot! }
}

describe('IsolatedSvg', () => {
  // jsdom never applies a <style> inside an <svg> (no stylesheet is registered), so these two check where the
  // stylesheet lives; the computed colours were measured in Chrome (交付/包E-图标着色.md).
  it('keeps an icon stylesheet inside the icon, off the page', () => {
    const { root } = page(LIVE_ICON)
    expect(root.querySelector('svg > style')!.textContent).toContain('svg { color: #d8e0ea; }')
    expect(document.querySelectorAll('style')).toHaveLength(0)
    expect(document.body.innerHTML).not.toContain('#d8e0ea')
  })

  it('keeps the stylesheet an icon colours its shapes with', () => {
    const { root } = page(CLASS_ICON)
    expect(root.querySelector('svg > style')!.textContent).toBe('.cls-1 { fill: none; stroke: #123456; stroke-width: 2; }')
    expect(root.querySelector('path.cls-1')!.getAttribute('d')).toBe('M4 12h16')
  })

  it('lets an icon without colours of its own follow the surrounding text colour', () => {
    const { root } = page('<svg viewBox="0 0 24 24" stroke="currentColor"><path d="M4 12h16"/></svg>')
    expect(getComputedStyle(root.querySelector('svg')!).color).toBe('rgb(1, 2, 3)')
  })

  it('redraws when the icon changes and drops scripts', async () => {
    const { wrapper, root } = page(LIVE_ICON)
    await wrapper.setData({ svg: '<svg onload="alert(1)"><script>alert(2)</script><path d="M0 0"/></svg>' })
    await nextTick()
    expect(root.querySelector('svg')!.outerHTML).toBe('<svg><path d="M0 0"></path></svg>')
  })
})
