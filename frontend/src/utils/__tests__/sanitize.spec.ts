import DOMPurify from 'dompurify'
import { describe, expect, it } from 'vitest'
import { sanitizeHtml, sanitizeSvg } from '../sanitize'

// The custom menu icon configured on the live site. Inlined with its <style>, that style was an unlayered stylesheet
// for the whole page and outranked every Tailwind colour utility on every SVG.
const LIVE_ICON = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor">' +
  '<style>svg { color: #526071; } @media (prefers-color-scheme: dark) { svg { color: #d8e0ea; } }</style>' +
  '<path d="M12 6v12m6-6H6"/></svg>'

describe('sanitizeSvg', () => {
  it('drops the <style> an icon carries and keeps the drawing', () => {
    // DOMPurify's SVG profile on its own keeps it: this is what reached the page.
    expect(DOMPurify.sanitize(LIVE_ICON, { USE_PROFILES: { svg: true, svgFilters: true } })).toContain('<style>')
    expect(sanitizeSvg(LIVE_ICON)).toBe(
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor">' +
      '<path d="M12 6v12m6-6H6"></path></svg>'
    )
  })

  it('still drops scripts and event handlers', () => {
    expect(sanitizeSvg('<svg onload="alert(1)"><script>alert(2)</script><path d="M0 0"/></svg>'))
      .toBe('<svg><path d="M0 0"></path></svg>')
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
