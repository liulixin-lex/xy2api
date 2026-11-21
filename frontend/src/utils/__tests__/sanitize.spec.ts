import DOMPurify from 'dompurify'
import { describe, expect, it } from 'vitest'
import { sanitizeHtml, sanitizeSvg } from '../sanitize'

describe('sanitizeSvg', () => {
  it('keeps the <style> an icon colours its shapes with and drops scripts and event handlers', () => {
    expect(sanitizeSvg('<svg onload="alert(1)"><style>.cls-1 { stroke: #123456; }</style><script>alert(2)</script><path class="cls-1" d="M0 0"/></svg>'))
      .toBe('<svg><style>.cls-1 { stroke: #123456; }</style><path class="cls-1" d="M0 0"></path></svg>')
  })
})

describe('sanitizeHtml', () => {
  it('drops <style> in HTML and in inline SVG, keeping the content around it', () => {
    const html = '<p>公告</p><style>svg { color: #d8e0ea; }</style><svg viewBox="0 0 1 1"><style>svg { color: red }</style></svg>'
    // DOMPurify's defaults keep a <style> that follows other content.
    expect(DOMPurify.sanitize(html)).toContain('<style>')
    expect(sanitizeHtml(html)).toBe('<p>公告</p><svg viewBox="0 0 1 1"></svg>')
  })

  it('keeps inline style attributes, which only touch their own element', () => {
    expect(sanitizeHtml('<span style="color: red">x</span>')).toBe('<span style="color: red">x</span>')
  })
})
