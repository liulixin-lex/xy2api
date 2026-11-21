import DOMPurify from 'dompurify'

// Uploaded SVG icons keep their <style>: exported icons often colour their shapes through classes (.cls-1 { stroke: … }),
// and without it they draw nothing. Scripts and event handlers still go. Such an icon must only be drawn through
// IsolatedSvg, whose shadow root keeps that stylesheet off the rest of the page.
export function sanitizeSvg(svg: string): string {
  if (!svg) return ''
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } })
}

// Admin text shown inside the app's own pages never keeps a <style> element. Inlined, it is a stylesheet for the whole
// page, and an unlayered one, so it outranks every Tailwind utility (those sit in @layer). Inline style attributes stay;
// they only touch their own element. Pages that are wholly the admin's (custom home, custom pages) keep theirs.
export function sanitizeHtml(html: string): string {
  return DOMPurify.sanitize(html, { FORBID_TAGS: ['style'] })
}
