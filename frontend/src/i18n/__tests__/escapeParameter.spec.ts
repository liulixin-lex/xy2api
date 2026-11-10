import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'

describe('i18n parameter escaping', () => {
  it('neutralizes iframe srcdoc when translations are rendered as HTML (GHSA-83rm-6c8r-g728)', () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'en',
      escapeParameter: true,
      warnHtmlMessage: false,
      messages: { en: { message: '<iframe srcdoc="{content}"></iframe>' } }
    })
    const html = i18n.global.t('message', { content: '<b>untrusted</b>' })
    const template = document.createElement('template')
    template.innerHTML = html

    expect(template.content.querySelector('iframe')).not.toBeNull()
    expect(template.content.querySelector('iframe')?.hasAttribute('srcdoc')).toBe(false)
  })
})
