import DOMPurify, { type Config } from 'dompurify'

// Markup inlined with v-html never keeps a <style> element. Inlined, it is a stylesheet for the whole page, and an
// unlayered one, so it outranks every Tailwind utility (those sit in @layer): a custom menu icon carrying
// `svg { color: … }` turned every coloured icon on the site that one colour. Inline style attributes stay; they only
// touch their own element.
const NO_STYLESHEETS = ['style']

export function sanitizeSvg(svg: string): string {
  if (!svg) return ''
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true }, FORBID_TAGS: NO_STYLESHEETS })
}

export function sanitizeHtml(html: string, config: Config = {}): string {
  return DOMPurify.sanitize(html, { ...config, FORBID_TAGS: [...(config.FORBID_TAGS ?? []), ...NO_STYLESHEETS] })
}
