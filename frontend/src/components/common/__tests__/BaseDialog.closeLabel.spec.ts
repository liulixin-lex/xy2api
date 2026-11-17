import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import BaseDialog from '../BaseDialog.vue'

enableAutoUnmount(afterEach)

describe('BaseDialog close button', () => {
  it('is named in the page language and follows a language switch', async () => {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: { common: { close: '关闭' } }, en: { common: { close: 'Close' } } } })
    mount(BaseDialog, { attachTo: document.body, props: { show: true, title: '晨星组' }, global: { plugins: [i18n] } })
    await nextTick()
    const button = () => document.querySelector('.modal-header button')!
    expect(button().getAttribute('aria-label')).toBe('关闭')
    i18n.global.locale.value = 'en'
    await nextTick()
    expect(button().getAttribute('aria-label')).toBe('Close')
  })
})
