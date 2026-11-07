import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import type { ApiKey, CustomEndpoint, GroupPlatform } from '@/types'
import CcSwitchImportDialog from '../CcSwitchImportDialog.vue'
import { GROK_CC_SWITCH_MODEL, OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const keyFor = (platform: GroupPlatform | null, name = 'laptop'): ApiKey =>
  ({
    id: 7,
    key: 'sk-abcdef0123456789',
    name,
    group: platform ? { id: 1, name: 'group', platform } : undefined
  }) as unknown as ApiKey

const mountDialog = (
  apiKey: ApiKey | null,
  options: { customEndpoints?: CustomEndpoint[]; siteName?: string } = {}
) =>
  mount(CcSwitchImportDialog, {
    props: {
      show: true,
      apiKey,
      baseUrl: 'https://api.example.com/',
      siteName: options.siteName ?? 'XY2API',
      customEndpoints: options.customEndpoints ?? []
    },
    global: {
      stubs: {
        BaseDialog: {
          name: 'BaseDialog',
          props: ['show', 'title', 'width'],
          emits: ['close'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        Icon: true
      }
    }
  })

const inputValue = (wrapper: ReturnType<typeof mountDialog>, test: string) =>
  (wrapper.get(`[data-test="${test}"]`).element as HTMLInputElement).value

describe('CcSwitchImportDialog', () => {
  it('prefills a Codex import with the key name, the site endpoint and the default model', () => {
    const wrapper = mountDialog(keyFor('openai'))

    expect(wrapper.get('[data-test="ccs-app"]').text()).toBe('Codex')
    expect(inputValue(wrapper, 'ccs-name')).toBe('XY2API · laptop')
    expect(inputValue(wrapper, 'ccs-endpoint')).toBe('https://api.example.com')
    expect(inputValue(wrapper, 'ccs-model')).toBe(OPENAI_CC_SWITCH_CODEX_MODEL)
    expect(wrapper.text()).toContain('sk-abc...6789')
  })

  it('emits the edited values, trimmed', async () => {
    const wrapper = mountDialog(keyFor('openai'))

    await wrapper.get('[data-test="ccs-name"]').setValue('  My Codex  ')
    await wrapper.get('[data-test="ccs-endpoint"]').setValue(' https://cn.example.com ')
    await wrapper.get('[data-test="ccs-model"]').setValue(' gpt-5.6-sol ')
    await wrapper.get('[data-test="ccs-import-confirm"]').trigger('click')

    expect(wrapper.emitted('import')).toEqual([
      [{ clientType: 'claude', providerName: 'My Codex', endpoint: 'https://cn.example.com', model: 'gpt-5.6-sol' }]
    ])
  })

  it('has no model field for Claude Code imports and emits no model', async () => {
    const wrapper = mountDialog(keyFor('anthropic'))

    expect(wrapper.get('[data-test="ccs-app"]').text()).toBe('Claude Code')
    expect(wrapper.find('[data-test="ccs-model"]').exists()).toBe(false)
    expect(inputValue(wrapper, 'ccs-endpoint')).toBe('https://api.example.com/')

    await wrapper.get('[data-test="ccs-import-confirm"]').trigger('click')
    expect(wrapper.emitted('import')?.[0]?.[0]).toMatchObject({ clientType: 'claude', model: undefined })
  })

  it('imports Gemini groups into Gemini CLI', async () => {
    const wrapper = mountDialog(keyFor('gemini'))

    expect(wrapper.get('[data-test="ccs-app"]').text()).toBe('Gemini CLI')
    await wrapper.get('[data-test="ccs-import-confirm"]').trigger('click')
    expect(wrapper.emitted('import')?.[0]?.[0]).toMatchObject({ clientType: 'gemini' })
  })

  it('lets Antigravity keys pick the client inside the panel', async () => {
    const wrapper = mountDialog(keyFor('antigravity'))

    expect(wrapper.get('[data-test="ccs-app"]').text()).toBe('Claude Code')
    expect(inputValue(wrapper, 'ccs-endpoint')).toBe('https://api.example.com/antigravity')

    await wrapper.get('[data-test="ccs-client-gemini"]').trigger('click')
    expect(wrapper.get('[data-test="ccs-app"]').text()).toBe('Gemini CLI')
    expect(wrapper.get('[data-test="ccs-client-gemini"]').attributes('aria-checked')).toBe('true')

    await wrapper.get('[data-test="ccs-import-confirm"]').trigger('click')
    expect(wrapper.emitted('import')?.[0]?.[0]).toMatchObject({
      clientType: 'gemini',
      endpoint: 'https://api.example.com/antigravity'
    })
  })

  it('offers admin-configured endpoints shaped for the app', async () => {
    const wrapper = mountDialog(keyFor('grok'), {
      customEndpoints: [
        { name: 'CN', endpoint: 'https://cn.example.com/', description: '' },
        { name: 'Duplicate', endpoint: 'https://api.example.com/v1', description: '' }
      ]
    })

    const choices = wrapper.findAll('[data-test="ccs-endpoint-choice"]')
    expect(choices.map((choice) => choice.text())).toEqual(['keys.ccsImport.endpointDefault', 'CN'])
    expect(inputValue(wrapper, 'ccs-model')).toBe(GROK_CC_SWITCH_MODEL)

    await choices[1].trigger('click')
    expect(inputValue(wrapper, 'ccs-endpoint')).toBe('https://cn.example.com/v1')
  })

  it('hides the quick picks when the admin configured no extra endpoint', () => {
    const wrapper = mountDialog(keyFor('openai'))

    expect(wrapper.findAll('[data-test="ccs-endpoint-choice"]')).toHaveLength(0)
  })

  it('blocks the import until name, endpoint and model are valid', async () => {
    const wrapper = mountDialog(keyFor('openai'))
    const confirm = () => wrapper.get('[data-test="ccs-import-confirm"]')

    await wrapper.get('[data-test="ccs-name"]').setValue('   ')
    expect(confirm().attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('keys.ccsImport.nameRequired')
    await wrapper.get('[data-test="ccs-name"]').setValue('Codex')

    await wrapper.get('[data-test="ccs-endpoint"]').setValue('api.example.com')
    expect(confirm().attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('keys.ccsImport.endpointInvalid')
    await wrapper.get('[data-test="ccs-endpoint"]').setValue('https://api.example.com')

    await wrapper.get('[data-test="ccs-model"]').setValue('')
    expect(confirm().attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('keys.ccsImport.modelRequired')

    await confirm().trigger('click')
    expect(wrapper.emitted('import')).toBeUndefined()
  })

  it('restores the defaults each time it opens', async () => {
    const wrapper = mountDialog(keyFor('openai'))

    await wrapper.get('[data-test="ccs-name"]').setValue('Edited')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(inputValue(wrapper, 'ccs-name')).toBe('XY2API · laptop')
  })

  it('uses the site name alone when the key has no name', () => {
    const wrapper = mountDialog(keyFor('openai', ''))

    expect(inputValue(wrapper, 'ccs-name')).toBe('XY2API')
  })

  it('emits close from the cancel button', async () => {
    const wrapper = mountDialog(keyFor('openai'))

    const cancel = wrapper.findAll('button').find((button) => button.text() === 'common.cancel')
    await cancel?.trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
