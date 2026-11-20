import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountNameCell from '../AccountNameCell.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

// Opus review, suggestion 1: on a touch screen a cut linked name took you to the site before you could read it.
const account = { id: 1, name: 'relay-account-with-a-long-name', platform: 'openai', type: 'apikey', credentials: { base_url: 'https://relay.example.com/v1' } }
const matchMedia = window.matchMedia
const pointer = (fine: boolean) => {
  window.matchMedia = ((query: string) => ({ matches: query.includes('pointer: fine') ? fine : !fine, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
}
afterEach(() => {
  window.matchMedia = matchMedia
  document.body.innerHTML = ''
})
const tapLink = (link: HTMLElement) => {
  const event = new MouseEvent('click', { bubbles: true, cancelable: true })
  link.dispatchEvent(event)
  return event.defaultPrevented
}

describe('AccountNameCell', () => {
  it('on touch shows the tip on the first tap on its link, and opens the site on the next', () => {
    pointer(false)
    const wrapper = mount(AccountNameCell, { attachTo: document.body, props: { account: account as never } })
    const link = wrapper.get('a').element as HTMLElement
    expect(tapLink(link)).toBe(true)
    expect(document.getElementById('sq-tip')!.hidden).toBe(false)
    expect(document.getElementById('sq-tip')!.textContent).toContain('https://relay.example.com')
    expect(tapLink(link)).toBe(false)
    wrapper.unmount()
  })

  it('with a mouse opens the site on the first click', () => {
    pointer(true)
    const wrapper = mount(AccountNameCell, { attachTo: document.body, props: { account: account as never } })
    expect(tapLink(wrapper.get('a').element as HTMLElement)).toBe(false)
    wrapper.unmount()
  })
})
