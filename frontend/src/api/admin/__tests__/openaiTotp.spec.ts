import { beforeEach, describe, expect, it, vi } from 'vitest'
import { openaiTotpAPI } from '../openaiTotp'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))
beforeEach(() => { vi.clearAllMocks(); for (const method of Object.values(client)) method.mockResolvedValue({ data: { task_id: 4 } }) })

describe('managed OpenAI 2FA API', () => {
  it('uses the contracted account routes, CAS revision and export opt-in', async () => {
    const signal = new AbortController().signal
    const credentials = { email: 'test@example.test', password: 'test-password', totp_secret: 'JBSWY3DPEHPK3PXP', revision: 7 }
    await openaiTotpAPI.status(3, signal)
    await openaiTotpAPI.save(3, credentials, signal)
    await openaiTotpAPI.rotate(3, signal)
    await openaiTotpAPI.reconcile(3, 4, 'verify_old', signal)
    await openaiTotpAPI.export(3, 'previous', false, signal)
    expect(client.get).toHaveBeenCalledWith('/admin/accounts/3/openai-totp', { signal })
    expect(client.put).toHaveBeenCalledWith('/admin/accounts/3/openai-totp', credentials, { signal, _retry: true })
    expect(client.post.mock.calls).toEqual([
      ['/admin/accounts/3/openai-totp/rotate', {}, { signal, _retry: true }],
      ['/admin/accounts/3/openai-totp/reconcile', { task_id: 4, action: 'verify_old' }, { signal, _retry: true }],
      ['/admin/accounts/3/openai-totp/export', { source: 'previous', include_password: false }, { signal, _retry: true }]
    ])
  })
  it('imports by task id without sending the session back through the browser', async () => {
    const credentials = { email: 'test@example.test', password: 'test-password', totp_secret: 'JBSWY3DPEHPK3PXP' }
    await openaiTotpAPI.login(credentials)
    await openaiTotpAPI.loginTask(4)
    await openaiTotpAPI.importLogin(4)
    expect(client.post).toHaveBeenCalledWith('/admin/accounts/openai-login', credentials, { signal: undefined, _retry: true })
    expect(client.get).toHaveBeenCalledWith('/admin/accounts/openai-login/4', { signal: undefined })
    expect(client.post).toHaveBeenCalledWith('/admin/accounts/openai-login/4/import', {}, { signal: undefined, _retry: true })
  })
  it('never retries a failed mutation within the API', async () => {
    client.post.mockRejectedValueOnce({ status: 503 })
    await expect(openaiTotpAPI.rotate(3)).rejects.toEqual({ status: 503 })
    expect(client.post).toHaveBeenCalledTimes(1)
  })
})
