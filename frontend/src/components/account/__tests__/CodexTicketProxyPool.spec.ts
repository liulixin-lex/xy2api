import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodexTicketProxyPool from '../CodexTicketProxyPool.vue'
const api = vi.hoisted(() => ({ getTicketProxyPool: vi.fn(), saveTicketProxyPool: vi.fn(), testTicketProxy: vi.fn() }))
vi.mock('@/api/admin/codexTicketProxies', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
const success = { success: true, latency_ms: 32, checked_at: '2026-09-20T00:00:00Z', ip_address: '2001:db8::7', country_code: 'CH' }
const pool = () => ({ revision: 'pool-r1', entries: [{ id: 'proxy-a', revision: 'r1', name: 'A', enabled: true, display: 'socks5h://example.test:1080', last_success: success }] })
const wrappers: ReturnType<typeof mount>[] = []
function card() { const w = mount(CodexTicketProxyPool); wrappers.push(w); return w }
function button(w: ReturnType<typeof mount>, text: string) { return w.findAll('button').find(b => b.text() === text)! }
const key = (name: string) => `admin.settings.stateProxies.${name}`
describe('CodexTicketProxyPool', () => {
 beforeEach(() => { vi.resetAllMocks(); api.getTicketProxyPool.mockResolvedValue(pool()) })
 afterEach(() => wrappers.splice(0).forEach(w => w.unmount()))
 it('keeps saved credentials blank and submits the list revision', async () => {
  const w = card(); await flushPromises()
  expect(w.get('input[type="password"]').element.value).toBe('')
  expect(w.html()).not.toContain('user:secret')
  await w.get('input[type="checkbox"]').setValue(false)
  api.saveTicketProxyPool.mockResolvedValue({ ...pool(), revision: 'r2' })
  await button(w,key('save')).trigger('click'); await flushPromises()
  expect(api.saveTicketProxyPool.mock.calls[0][0]).toMatchObject({ revision: 'pool-r1', entries: [{ id: 'proxy-a', enabled: false, url: '' }] })
 })
 it('shows a failed current sample separately from the last successful exit', async () => {
  api.testTicketProxy.mockResolvedValue({ success: false, checked_at: '2026-09-20T01:00:00Z', latency_ms: 10000 })
  const w = card(); await flushPromises(); await button(w,key('test')).trigger('click'); await flushPromises()
  expect(w.text()).toContain(key('failed')); expect(w.text()).toContain(key('lastSuccess')); expect(w.text()).toContain('2001:db8::7')
 })
 it('invalidates old results immediately on address edit and ignores an old probe response', async () => {
  let resolve!: (v: typeof success) => void
  api.testTicketProxy.mockReturnValue(new Promise(r => { resolve = r }))
  const w = card(); await flushPromises(); await button(w,key('test')).trigger('click')
  await w.get('input[type="password"]').setValue('http://new.example:8080')
  resolve(success); await flushPromises(); expect(w.text()).not.toContain('2001:db8::7')
 })
 it('does not automatically overwrite another administrator after a revision conflict', async () => {
  api.saveTicketProxyPool.mockRejectedValue({ status: 409 })
  const w = card(); await flushPromises(); await w.get('input[type="checkbox"]').setValue(false)
  await button(w,key('save')).trigger('click'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toContain(key('conflict')); expect(api.saveTicketProxyPool).toHaveBeenCalledTimes(1)
  expect(w.get('input[type="checkbox"]').element.checked).toBe(false)
 })
 it('allows an explicit empty pool and renders the empty state', async () => {
  const w = card(); await flushPromises(); await button(w,'common.delete').trigger('click')
  api.saveTicketProxyPool.mockResolvedValue({ revision: 'r2', entries: [] })
  await button(w,key('save')).trigger('click'); await flushPromises()
  expect(api.saveTicketProxyPool).toHaveBeenCalledWith({ revision: 'pool-r1', entries: [] }); expect(w.text()).toContain(key('empty'))
 })
})
