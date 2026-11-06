import { flushPromises, mount } from '@vue/test-utils'
import { reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MCPConnectionsCard from '../MCPConnectionsCard.vue'

const { listMock, revokeMock, state } = vi.hoisted(() => ({
  listMock: vi.fn(), revokeMock: vi.fn(), state: { auth: { user: { id: 1 } } }
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('vue-i18n', async (original) => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, locale: ref('en') })
}))
vi.mock('../api', async (original) => ({
  ...await original<typeof import('../api')>(), listConnections: listMock, revokeConnection: revokeMock
}))
const result = () => ({
  resource: 'https://gateway.example.test/mcp',
  items: [{ id: 'grant-one', client_id: 'client-one', client_name: 'My client',
    resource: 'https://gateway.example.test/mcp', tools: ['xy2api.me.get_profile'],
    created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 60_000).toISOString(), revoked_at: null }]
})
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const wrapper = mount(MCPConnectionsCard)
  wrappers.push(wrapper)
  return wrapper
}
function button(wrapper: ReturnType<typeof render>, label: string) {
  return wrapper.findAll('button').find((item) => item.text() === `mcp.${label}`)!
}
beforeEach(() => {
  listMock.mockReset().mockResolvedValue(result())
  revokeMock.mockReset()
  state.auth = reactive({ user: { id: 1 } })
})
afterEach(() => wrappers.splice(0).forEach((wrapper) => wrapper.unmount()))

describe('MCP connection management', () => {
  it('hides only an explicit disabled response, and exposes service errors for retry', async () => {
    listMock.mockRejectedValueOnce({ status: 404 })
    const disabled = render()
    await flushPromises()
    expect(disabled.find('section').exists()).toBe(false)
    listMock.mockRejectedValueOnce({ status: 503 })
    const failed = render()
    await flushPromises()
    expect(failed.text()).toContain('mcp.connectionsFailed')
    expect(button(failed, 'retry').exists()).toBe(true)
  })

  it('requires confirmation, retains access on a failed revoke, and updates only after success', async () => {
    const wrapper = render()
    await flushPromises()
    await button(wrapper, 'revoke').trigger('click')
    expect(revokeMock).not.toHaveBeenCalled()
    revokeMock.mockRejectedValueOnce(new Error('Unavailable'))
    await button(wrapper, 'confirmRevoke').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('mcp.revokeFailed')
    expect(wrapper.text()).toContain('mcp.status.active')
    revokeMock.mockResolvedValueOnce(undefined)
    await button(wrapper, 'confirmRevoke').trigger('click')
    await flushPromises()
    expect(revokeMock).toHaveBeenCalledWith('grant-one', expect.any(AbortSignal))
    expect(wrapper.text()).toContain('mcp.status.revoked')
    expect(wrapper.text()).not.toContain('mcp.status.active')
  })

  it('discards connections from a previous account while loading the next account', async () => {
    let finishOld!: (value: ReturnType<typeof result>) => void
    listMock.mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve }))
    const wrapper = render()
    listMock.mockResolvedValue({ resource: result().resource, items: [] })
    state.auth.user.id = 2
    await flushPromises()
    finishOld(result())
    await flushPromises()
    expect(listMock.mock.calls[0][0].aborted).toBe(true)
    expect(wrapper.text()).toContain('mcp.noConnections')
    expect(wrapper.text()).not.toContain('My client')
  })
})
