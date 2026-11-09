import katex from 'katex'
import mermaid from 'mermaid'

// Run in a native browser: jsdom does not validate CSS declaration values.
export async function runRenderingBrowserRegression() {
  const host = document.createElement('div')
  document.body.append(host)
  const originalAlert = window.alert
  let executedScripts = 0
  window.alert = () => { executedScripts++ }
  const results: string[] = []
  function check(value: unknown, message: string): asserts value {
    if (!value) throw new Error(message)
  }
  function mount(markup: string) {
    const container = document.createElement('section')
    host.append(container)
    container.innerHTML = markup
    check(!container.querySelector('script, iframe, object, embed, img'), 'active element')
    for (const node of container.querySelectorAll('*')) {
      for (const attribute of node.attributes) {
        check(!/^on/i.test(attribute.name), 'event handler')
        if (['href', 'xlink:href', 'src'].includes(attribute.name)) {
          check(!/^(?:javascript:|vbscript:|data:text\/html)/i.test(attribute.value.trim()), 'active URL')
        }
      }
    }
    return container
  }
  try {
    const formula = mount(katex.renderToString(String.raw`x=\frac{-b\pm\sqrt{b^2-4ac}}{2a}`, { trust: false }))
    check(formula.querySelector('math mfrac') && formula.querySelector('math msqrt'), 'formula structure')
    results.push('formula')
    const text = mount(katex.renderToString(String.raw`\text{<img src=x onerror=alert(1)>}`, { trust: false }))
    check(text.querySelector('.katex-html')?.textContent?.replace(/\u00a0/g, ' ').includes('<img src=x onerror=alert(1)>'), 'formula escaping')
    results.push('formula HTML escaping')
    const options = Object.assign(Object.create({ trust: true }), { throwOnError: false })
    check(!mount(katex.renderToString(String.raw`\href{javascript:alert(1)}{click}`, options)).querySelector('a'), 'inherited trust')
    results.push('KaTeX inherited trust')
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', htmlLabels: false })
    const normal = mount((await mermaid.render('regression-normal', 'flowchart TD\n A[Request] --> B[Response]')).svg)
    check(normal.querySelectorAll('.node').length === 2 && normal.querySelector('.edgePath, .flowchart-link'), 'flowchart structure')
    results.push('flowchart')
    mount((await mermaid.render('regression-hostile-label', 'flowchart TD\n A["Safe <img src=x onerror=alert(1)><script>alert(1)</script>"] --> B[Done]')).svg)
    results.push('diagram HTML escaping')
    const state = 'stateDiagram-v2\n classDef unsafe fill:red</style></svg><div id="injected">Injected</div><svg><style>a:b\n [*] --> A:::unsafe'
    const content = mount((await mermaid.render('regression-classdef', state)).svg)
    check(content.children.length === 1 && content.firstElementChild?.tagName.toLowerCase() === 'svg', 'SVG boundary')
    check(!content.querySelector('#injected'), 'classDef DOM injection')
    results.push('state classDef containment')
    await new Promise(resolve => setTimeout(resolve, 50))
    check(executedScripts === 0, 'malicious script executed')
    // Positive control: prove native inline event handlers can execute in this harness.
    const control = document.createElement('img')
    control.setAttribute('onerror', "this.dataset.executed='yes'")
    control.dispatchEvent(new Event('error'))
    check(control.dataset.executed === 'yes', 'script execution control failed')
    return { results, executedScripts, executionControl: true }
  } finally {
    host.remove()
    window.alert = originalAlert
  }
}
