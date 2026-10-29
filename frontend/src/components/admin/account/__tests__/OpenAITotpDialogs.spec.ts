import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import OpenAILoginImportDialog from '../OpenAILoginImportDialog.vue'
import OpenAITotpDialog from '../OpenAITotpDialog.vue'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'
import type { OpenAITotpExport, OpenAITotpStatus, OpenAITotpTask } from '@/api/admin/openaiTotp'

const mocks = vi.hoisted(() => ({
  auth: { user: { id: 1, role: 'admin' }, isAdmin: true },
  api: { status: vi.fn(), save: vi.fn(), rotate: vi.fn(), reconcile: vi.fn(), export: vi.fn(), login: vi.fn(), loginTask: vi.fn(), importLogin: vi.fn() },
  qr: vi.fn()
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/api/admin/openaiTotp', () => ({ openaiTotpAPI: mocks.api }))
vi.mock('qrcode', () => ({ default: { toDataURL: mocks.qr } }))
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key })
}))

const account = (id = 5, extra: Partial<Account> = {}) => ({ id, name: `account-${id}`, platform: 'openai', type: 'oauth', parent_account_id: null, ...extra }) as Account
const task = (state: OpenAITotpTask['state'] = 'queued', extra: Partial<OpenAITotpTask> = {}): OpenAITotpTask => ({ task_id: 13, state, action: 'rotate', ...extra })
const status = (extra: Partial<OpenAITotpStatus> = {}): OpenAITotpStatus => ({ configured: true, email: 'openai@example.test', revision: 4, worker_ready: true, locked: false, task: null, ...extra })
const secret = 'JBSWY3DPEHPK3PXP'
const exportData = (extra: Partial<OpenAITotpExport> = {}): OpenAITotpExport => ({ email: 'openai@example.test', secret, otpauth_uri: `otpauth://totp/OpenAI:test?secret=${secret}`, state: 'succeeded', source: 'current', ...extra })
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: unknown) => void; const promise = new Promise<T>((ok, no) => { resolve = ok; reject = no }); return { promise, resolve, reject } }

const globals = { stubs: {
  BaseDialog: { props: ['show'], emits: ['close'], template: '<section v-if="show"><slot /><slot name="footer" /><button data-test="close" @click="$emit(\'close\')">close</button></section>' },
  TotpStepUpDialog: { props: ['controller'], template: '<div v-if="controller.visible.value" data-test="step-up"><button data-test="verified" @click="controller.onVerified()">verify</button><button data-test="cancel-step-up" @click="controller.onCancel()">cancel</button></div>' },
  Teleport: true,
  Icon: true
} }
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  mocks.auth = reactive({ user: { id: 1, role: 'admin' }, isAdmin: true })
  mocks.api.status.mockResolvedValue(status())
  mocks.api.login.mockResolvedValue(task('queued', { action: 'login' }))
  mocks.api.loginTask.mockResolvedValue(task('ready', { action: 'login' }))
  mocks.api.importLogin.mockResolvedValue({ account_id: 99 })
  mocks.api.save.mockResolvedValue(status({ revision: 5 }))
  mocks.api.rotate.mockResolvedValue(task('queued'))
  mocks.api.reconcile.mockResolvedValue(task('queued'))
  mocks.api.export.mockResolvedValue(exportData())
  mocks.qr.mockResolvedValue('data:image/png;base64,cXJjb2Rl')
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
const login = () => mount(OpenAILoginImportDialog, { props: { show: true }, global: globals })
const manage = () => mount(OpenAITotpDialog, { props: { show: true, account: account() }, global: globals })
async function credentials(wrapper: ReturnType<typeof login> | ReturnType<typeof manage>) {
  await wrapper.get('[data-test="email"]').setValue(' openai@example.test ')
  await wrapper.get('[data-test="password"]').setValue('private-password')
  await wrapper.get('[data-test="secret"]').setValue('jbsw y3dp ehpk 3pxp')
}

describe('OpenAI login import', () => {
  it('queues once, polls GET and imports on the server after login is ready', async () => {
    const wrapper = login()
    expect(wrapper.get('[data-test="login"]').attributes('disabled')).toBeDefined()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.api.login).toHaveBeenCalledTimes(1)
    expect(mocks.api.login).toHaveBeenCalledWith({ email: 'openai@example.test', password: 'private-password', totp_secret: secret }, expect.any(AbortSignal))
    expect(wrapper.find('[data-test="password"]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(mocks.api.loginTask).toHaveBeenCalledWith(13, expect.any(AbortSignal))
    await wrapper.get('[data-test="import"]').trigger('click')
    await flushPromises()
    expect(mocks.api.importLogin).toHaveBeenCalledWith(13, expect.any(AbortSignal))
    expect(wrapper.emitted('imported')).toEqual([[99]])
    expect(wrapper.html()).not.toContain(secret)
    expect(wrapper.html()).not.toContain('private-password')
  })

  it('resumes an existing task with GET without resubmitting login credentials', async () => {
    const wrapper = login()
    await wrapper.get('[data-test="resume-id"]').setValue(13)
    await wrapper.get('[data-test="resume"]').trigger('click')
    await flushPromises()
    expect(mocks.api.loginTask).toHaveBeenCalledWith(13, expect.any(AbortSignal))
    expect(mocks.api.login).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="import"]').exists()).toBe(true)
  })

  it('prompts for step-up and retries only after a grant', async () => {
    mocks.api.login.mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' })
    const wrapper = login()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.api.login).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-test="step-up"]').exists()).toBe(true)
    await wrapper.get('[data-test="verified"]').trigger('click')
    await flushPromises()
    expect(mocks.api.login).toHaveBeenCalledTimes(2)
  })

  it('cancels pending step-up on close and never replays the write', async () => {
    mocks.api.login.mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' })
    const wrapper = login()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('[data-test="close"]').trigger('click')
    await wrapper.setProps({ show: false })
    await flushPromises()
    await wrapper.setProps({ show: true })
    expect(mocks.api.login).toHaveBeenCalledTimes(1)
    expect((wrapper.get('[data-test="secret"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[data-test="step-up"]').exists()).toBe(false)
  })

  it('aborts on close, ignores a late task response and clears fields on reopening', async () => {
    const pending = deferred<OpenAITotpTask>()
    mocks.api.login.mockReturnValueOnce(pending.promise)
    const wrapper = login()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    const signal = mocks.api.login.mock.calls[0][1] as AbortSignal
    await wrapper.setProps({ show: false })
    expect(signal.aborted).toBe(true)
    pending.resolve(task('ready'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)
    await wrapper.setProps({ show: true })
    expect(wrapper.find('[data-test="task-state"]').exists()).toBe(false)
    expect((wrapper.get('[data-test="email"]').element as HTMLInputElement).value).toBe('')
    expect(mocks.api.loginTask).not.toHaveBeenCalled()
  })

  it('stops polling on unmount and drops a late failure', async () => {
    const pending = deferred<OpenAITotpTask>()
    const wrapper = login()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    mocks.api.loginTask.mockReturnValueOnce(pending.promise)
    await vi.advanceTimersByTimeAsync(2000)
    wrapper.unmount()
    expect((mocks.api.loginTask.mock.calls[0][1] as AbortSignal).aborted).toBe(true)
    pending.reject(new Error(secret))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(mocks.api.loginTask).toHaveBeenCalledTimes(1)
  })

  it('shows a safe worker message and never retries a failed POST automatically', async () => {
    mocks.api.login.mockRejectedValueOnce({ code: 'OPENAI_TOTP_WORKER_UNAVAILABLE', message: secret })
    const wrapper = login()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(mocks.api.login).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="alert"]').text()).toContain('workerUnavailable')
    expect(wrapper.html()).not.toContain(secret)
  })
})

describe('OpenAI 2FA management', () => {
  it('saves complete credentials with the revision and clears sensitive inputs', async () => {
    const wrapper = manage()
    await flushPromises()
    await credentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.api.save).toHaveBeenCalledWith(5, { email: 'openai@example.test', password: 'private-password', totp_secret: secret, revision: 4 }, expect.any(AbortSignal))
    expect((wrapper.get('[data-test="secret"]').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('[data-test="password"]').element as HTMLInputElement).value).toBe('')
  })

  it.each(['succeeded', 'failed'] as const)('shows the latest %s rotation task and stops polling', async state => {
    mocks.api.status.mockResolvedValue(status({ task: task(state, { error_code: 'mfa_rejected' }) }))
    const wrapper = manage()
    await flushPromises()
    expect(wrapper.get('[data-test="task-state"]').text()).toContain(state)
    expect(wrapper.get('fieldset').attributes('disabled')).toBeUndefined()
    if (state === 'failed') expect(wrapper.text()).toContain('taskErrors.mfa_rejected')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(mocks.api.status).toHaveBeenCalledTimes(1)
  })

  it('requires confirmation for rotation and disables it without a worker', async () => {
    mocks.api.status.mockResolvedValue(status({ worker_ready: false }))
    const wrapper = manage()
    await flushPromises()
    await wrapper.get('[data-test="rotation-confirm"]').setValue(true)
    expect(wrapper.get('[data-test="rotate"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('workerUnavailable')
    mocks.api.status.mockResolvedValue(status())
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    mocks.api.status.mockResolvedValue(status({ locked: true, task: task('running') }))
    await wrapper.get('[data-test="rotate"]').trigger('click')
    await flushPromises()
    expect(mocks.api.rotate).toHaveBeenCalledTimes(1)
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
  })

  it('locks uncertain tasks and offers only candidate/previous exports and verification', async () => {
    mocks.api.status.mockResolvedValue(status({ locked: true, task: task('uncertain', { has_candidate: true }) }))
    const wrapper = manage()
    await flushPromises()
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('[data-test="export-source"] option').map(option => option.attributes('value'))).toEqual(['candidate', 'previous'])
    await wrapper.get('[data-test="verify-new"]').trigger('click')
    await flushPromises()
    expect(mocks.api.reconcile).toHaveBeenCalledWith(5, 13, 'verify_new', expect.any(AbortSignal))
  })

  it('allows previous-key recovery even when candidate material is missing', async () => {
    mocks.api.status.mockResolvedValue(status({ locked: true, task: task('uncertain', { has_candidate: false }) }))
    const wrapper = manage()
    await flushPromises()
    expect(wrapper.get('[data-test="verify-new"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="verify-old"]').trigger('click')
    await flushPromises()
    expect(mocks.api.reconcile).toHaveBeenCalledWith(5, 13, 'verify_old', expect.any(AbortSignal))
  })

  it('defaults to password-free JSON and generates a QR locally only on opt-in', async () => {
    vi.useRealTimers()
    mocks.api.export.mockResolvedValue(exportData({ password: 'should-be-stripped' }))
    const createURL = vi.fn().mockReturnValue('blob:local')
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createURL })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = manage()
    await flushPromises()
    await wrapper.get('[data-test="export"]').trigger('click')
    await flushPromises()
    expect(mocks.api.export).toHaveBeenCalledWith(5, 'current', false, expect.any(AbortSignal))
    expect(mocks.qr).not.toHaveBeenCalled()
    await wrapper.get('[data-test="download"]').trigger('click')
    const blob = createURL.mock.calls[0][0] as Blob
    const content = await new Promise<string>(resolve => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.readAsText(blob) })
    expect(JSON.parse(content)).toEqual(exportData())
    await wrapper.get('[data-test="show-qr"]').setValue(true)
    await flushPromises()
    expect(mocks.qr).toHaveBeenCalledWith(exportData().otpauth_uri, expect.any(Object))
    expect(wrapper.find('img').exists()).toBe(true)
    await wrapper.get('[data-test="include-password"]').setValue(true)
    expect(wrapper.find('[data-test="exported"]').exists()).toBe(false)
    await wrapper.get('[data-test="export"]').trigger('click')
    await flushPromises()
    expect(mocks.api.export).toHaveBeenLastCalledWith(5, 'current', true, expect.any(AbortSignal))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:local')
  })

  it('ignores exports and QR responses after switching the managed account', async () => {
    const pendingExport = deferred<OpenAITotpExport>()
    const wrapper = manage()
    await flushPromises()
    mocks.api.export.mockReturnValueOnce(pendingExport.promise)
    await wrapper.get('[data-test="export"]').trigger('click')
    await wrapper.setProps({ account: account(6) })
    pendingExport.resolve(exportData())
    await flushPromises()
    expect(wrapper.find('[data-test="exported"]').exists()).toBe(false)
    await wrapper.get('[data-test="export"]').trigger('click')
    await flushPromises()
    const pendingQR = deferred<string>()
    mocks.qr.mockReturnValueOnce(pendingQR.promise)
    await wrapper.get('[data-test="show-qr"]').setValue(true)
    await wrapper.setProps({ show: false })
    pendingQR.resolve('data:image/png;base64,c2VjcmV0')
    await flushPromises()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.html()).not.toContain(secret)
  })

  it('clears secrets on an administrator session switch', async () => {
    const wrapper = manage()
    await flushPromises()
    await credentials(wrapper)
    await wrapper.get('[data-test="export"]').trigger('click')
    await flushPromises()
    mocks.auth.user = { id: 2, role: 'admin' }
    await flushPromises()
    expect((wrapper.get('[data-test="password"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[data-test="exported"]').exists()).toBe(false)
  })
})

describe('OpenAI 2FA access', () => {
  it('hides both dialogs from operators even when mounted directly', async () => {
    mocks.auth.isAdmin = false
    mocks.auth.user.role = 'operator'
    const first = login(), second = manage()
    await flushPromises()
    expect(first.find('section').exists()).toBe(false)
    expect(second.find('section').exists()).toBe(false)
    expect(mocks.api.status).not.toHaveBeenCalled()
  })
  it.each([
    ['admin', {}, true], ['operator', {}, false], ['admin', { parent_account_id: 1 }, false],
    ['admin', { type: 'apikey' }, false], ['admin', { platform: 'anthropic' }, false]
  ] as const)('limits the management menu to an OpenAI OAuth parent: %s %j', async (role, extra, allowed) => {
    mocks.auth.isAdmin = role === 'admin'
    const wrapper = mount(AccountActionMenu, { props: { show: true, account: account(5, extra), anchorRect: new DOMRect(10, 10, 20, 20) }, global: globals })
    expect(wrapper.find('[data-test="manage-openai-totp"]').exists()).toBe(allowed)
    if (allowed) {
      await wrapper.get('[data-test="manage-openai-totp"]').trigger('click')
      expect(wrapper.emitted('manage-openai-totp')?.[0]).toEqual([account()])
    }
  })
})
