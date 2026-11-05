import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MCPAuthorizeView from '../MCPAuthorizeView.vue'
import { verifiedConsentRedirect, type ConsentPreview } from '../api'

const { previewMock, decideMock, state } = vi.hoisted(() => ({
  previewMock: vi.fn(),
  decideMock: vi.fn(),
  state: { route: { query: { request_id: 'request-one' } }, auth: { user: { id: 1, email: 'user@example.test' } } }
}))
vi.mock('vue-router', () => ({ useRoute: () => state.route }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('vue-i18n', async (original) => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('../api', async (original) => ({
  ...await original<typeof import('../api')>(),
  previewConsent: previewMock,
  decideConsent: decideMock
}))

const data = (): ConsentPreview => ({
  request_id: 'request-one', client_id: 'client-one', client_name: '<script>untrusted</script>',
  client_name_verified: false, redirect_uri: 'https://client.example.test/callback',
  resource: 'https://gateway.example.test/mcp',
  tools: ['xy2api.me.get_profile', 'xy2api.me.search_usage'],
  expires_at: new Date(Date.now() + 60_000).toISOString()
})
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const wrapper = mount(MCPAuthorizeView, {
    global: { stubs: { AuthLayout: { template: '<main><slot /></main>' }, RouterLink: true } }
  })
  wrappers.push(wrapper)
  return wrapper
}
function button(wrapper: ReturnType<typeof render>, label: string) {
  return wrapper.findAll('button').find((item) => item.text() === `mcp.${label}`)!
}

beforeEach(() => {
  previewMock.mockReset().mockResolvedValue(data())
  decideMock.mockReset()
  state.route = reactive({ query: { request_id: 'request-one' } })
  state.auth = reactive({ user: { id: 1, email: 'user@example.test' } })
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.useRealTimers()
})

describe('MCP consent', () => {
  it('renders client text safely and submits only the selected tools', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.text()).toContain('<script>untrusted</script>')
    await wrapper.findAll('input[type=checkbox]')[1].setValue(false)
    decideMock.mockResolvedValue({ redirect_url: 'https://attacker.example.test/callback' })
    await button(wrapper, 'approve').trigger('click')
    await flushPromises()
    expect(decideMock).toHaveBeenCalledWith('request-one', true, ['xy2api.me.get_profile'], expect.any(AbortSignal))
    expect(wrapper.text()).toContain('mcp.redirectFailed')
    expect(button(wrapper, 'approve').attributes('disabled')).toBeDefined()
  })

  it('allows denial with no selected tool and never sends consent on preview alone', async () => {
    const wrapper = render()
    await flushPromises()
    expect(decideMock).not.toHaveBeenCalled()
    for (const input of wrapper.findAll('input[type=checkbox]')) await input.setValue(false)
    expect(button(wrapper, 'approve').attributes('disabled')).toBeDefined()
    decideMock.mockRejectedValue(new Error('Network unavailable'))
    await button(wrapper, 'deny').trigger('click')
    await flushPromises()
    // The API adapter discards tools on denial; the page also passes the empty selection.
    expect(decideMock).toHaveBeenCalledWith('request-one', false, [], expect.any(AbortSignal))
    expect(wrapper.text()).toContain('mcp.decisionFailed')
  })

  it('discards a stale preview when the authorization request changes', async () => {
    let finishOld!: (value: ConsentPreview) => void
    previewMock.mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve }))
    const wrapper = render()
    state.route.query.request_id = 'request-two'
    previewMock.mockResolvedValue({ ...data(), request_id: 'request-two', client_name: 'New client' })
    await flushPromises()
    finishOld(data())
    await flushPromises()
    expect(wrapper.text()).toContain('New client')
    expect(wrapper.text()).not.toContain('<script>untrusted</script>')
    expect(previewMock.mock.calls[0][1].aborted).toBe(true)
  })

  it('blocks submission after the pending request expires', async () => {
    vi.useFakeTimers()
    previewMock.mockResolvedValue({ ...data(), expires_at: new Date(Date.now() + 1_000).toISOString() })
    const wrapper = render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1_001)
    expect(button(wrapper, 'approve').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('mcp.expiredRequest')
    expect(decideMock).not.toHaveBeenCalled()
  })
})

describe('consent return address', () => {
  it('accepts registered HTTPS and loopback callbacks while preserving query values', () => {
    expect(verifiedConsentRedirect('https://client.example.test/cb?tenant=one&code=opaque', 'https://client.example.test/cb?tenant=one')).toBeTruthy()
    expect(verifiedConsentRedirect('http://127.0.0.1:8765/cb?error=access_denied', 'http://127.0.0.1:8765/cb')).toBeTruthy()
    expect(verifiedConsentRedirect('http://127.0.0.2:8765/cb?code=opaque', 'http://127.0.0.2:8765/cb')).toBeTruthy()
    expect(verifiedConsentRedirect('http://[::1]:8765/cb?code=opaque', 'http://[::1]:8765/cb')).toBeTruthy()
  })
  it.each([
    ['javascript:alert(1)', 'https://client.example.test/cb'],
    ['https://evil.example.test/cb', 'https://client.example.test/cb'],
    ['https://client.example.test/other', 'https://client.example.test/cb'],
    ['https://user:pass@client.example.test/cb', 'https://client.example.test/cb'],
    ['https://client.example.test/cb#token', 'https://client.example.test/cb'],
    ['https://client.example.test/cb?tenant=two', 'https://client.example.test/cb?tenant=one'],
    ['http://client.example.test/cb', 'http://client.example.test/cb'],
    ['http://127.0.0.1:9999/cb', 'http://127.0.0.1:8765/cb']
  ])('rejects unsafe or changed callbacks: %s', (candidate, registered) => {
    expect(verifiedConsentRedirect(candidate, registered)).toBeNull()
  })
})
