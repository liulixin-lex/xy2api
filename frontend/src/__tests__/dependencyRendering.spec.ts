import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import katex from 'katex'
import mermaid from 'mermaid'

const originalBBox = Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getBBox')
const originalTextLength = Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getComputedTextLength')

beforeAll(() => {
  // jsdom has no SVG text geometry; rendering and sanitization use the real libraries.
  Object.defineProperty(SVGElement.prototype, 'getBBox', {
    configurable: true,
    value: () => ({ x: 0, y: 0, width: 100, height: 20 })
  })
  Object.defineProperty(SVGElement.prototype, 'getComputedTextLength', {
    configurable: true,
    value: function (this: SVGElement) { return (this.textContent?.length ?? 0) * 8 }
  })
  mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', htmlLabels: false })
})

afterAll(() => {
  if (originalBBox) Object.defineProperty(SVGElement.prototype, 'getBBox', originalBBox)
  else Reflect.deleteProperty(SVGElement.prototype, 'getBBox')
  if (originalTextLength) Object.defineProperty(SVGElement.prototype, 'getComputedTextLength', originalTextLength)
  else Reflect.deleteProperty(SVGElement.prototype, 'getComputedTextLength')
})

function parseInertMarkup(markup: string): DocumentFragment {
  const template = document.createElement('template')
  template.innerHTML = markup
  expect(template.content.querySelector('script, iframe, object, embed, img')).toBeNull()
  for (const element of template.content.querySelectorAll('*')) {
    for (const attribute of element.attributes) {
      expect(attribute.name).not.toMatch(/^on/i)
      if (['href', 'xlink:href', 'src'].includes(attribute.name)) {
        expect(attribute.value.trim()).not.toMatch(/^(?:javascript:|vbscript:|data:text\/html)/i)
      }
    }
  }
  return template.content
}

describe('installed rendering dependencies', () => {
  it('renders a quadratic formula as accessible MathML and HTML', () => {
    const expression = String.raw`x = \frac{-b \pm \sqrt{b^2-4ac}}{2a}`
    const html = katex.renderToString(expression, { displayMode: true, trust: false })
    const content = parseInertMarkup(html)
    expect(content.querySelector('math mfrac')).not.toBeNull()
    expect(content.querySelector('math msqrt')).not.toBeNull()
    expect(content.querySelector('.katex-html')).not.toBeNull()
    expect(content.querySelector('annotation')?.textContent).toBe(expression)
  })

  it('escapes HTML in formula text instead of creating active elements', () => {
    const html = katex.renderToString(String.raw`\text{<img src=x onerror=alert(1)>}`, { trust: false })
    const content = parseInertMarkup(html)
    expect(content.querySelector('.katex-html')?.textContent?.replace(/\u00a0/g, ' ')).toContain('<img src=x onerror=alert(1)>')
  })

  it('ignores inherited trust when rendering an untrusted URL (GHSA-238p-pmpm-9mq7)', () => {
    const options = Object.assign(Object.create({ trust: true }), { throwOnError: false })
    const html = katex.renderToString(String.raw`\href{javascript:alert(1)}{click}`, options)
    expect(parseInertMarkup(html).querySelector('a')).toBeNull()
  })

  it('renders a flowchart with its nodes and connecting edge', async () => {
    const { svg } = await mermaid.render('normal-diagram', 'flowchart TD\n A[Request] --> B[Response]')
    const content = parseInertMarkup(svg)
    expect(content.querySelectorAll('.node')).toHaveLength(2)
    expect(content.querySelector('.edgePath, .flowchart-link')).not.toBeNull()
    expect(content.textContent).toContain('Request')
    expect(content.textContent).toContain('Response')
  })

  it('sanitizes script and event attributes in diagram labels', async () => {
    const { svg } = await mermaid.render('hostile-label', `flowchart TD\n A["Safe <img src=x onerror=alert(1)><script>alert(1)</script>"] --> B[Done]`)
    const content = parseInertMarkup(svg)
    expect(content.textContent).toContain('Safe')
    expect(content.textContent).toContain('Done')
  })

  // Mermaid classDef containment needs a real browser (jsdom does not validate CSS values);
  // it was verified out of tree for this upgrade.
})
