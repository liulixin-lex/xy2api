import DOMPurify from 'dompurify'

// Icons and admin text shown inside the app's own pages never keep a <style> element. Inlined, it is a stylesheet for
// the whole page, and an unlayered one, so it outranks every Tailwind utility (those sit in @layer): a custom menu
// icon carrying `svg { color: … }` turned every coloured icon on the site that one colour. Inline style attributes
// stay; they only touch their own element. Pages that are wholly the admin's (custom home, custom pages) keep theirs.
const NO_STYLESHEETS = ['style']

export function sanitizeSvg(svg: string): string {
  if (!svg) return ''
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true }, FORBID_TAGS: NO_STYLESHEETS })
}

export function sanitizeHtml(html: string): string {
  return DOMPurify.sanitize(html, { FORBID_TAGS: NO_STYLESHEETS })
}
