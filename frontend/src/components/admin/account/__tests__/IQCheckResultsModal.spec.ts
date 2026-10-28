import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import IQCheckResultsModal from '../IQCheckResultsModal.vue'
import type { Account, IQAttempt, IQRound, IQTarget } from '@/types'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
vi.mock('@/api/admin/accounts', () => ({ downloadIQCheckDiagnostics: vi.fn() }))
const { targetsAPI, roundsAPI, run } = vi.hoisted(() => ({ targetsAPI: vi.fn(), roundsAPI: vi.fn(), run: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getIQCheck: targetsAPI, listIQResults: roundsAPI, runIQCheck: run } } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (s: string, p?: Record<string, unknown>) => p ? `${s}${JSON.stringify(p)}` : s, te: () => true }) }))

const target = (model: string, patch: Partial<IQTarget> = {}): IQTarget => ({ model, enabled: true, reasoning_effort: 'low', interval_minutes: 5, timeout_seconds: 120, verdict: 'smart', verdict_at: null, gate_active: false, gate_model: null, gate_reset_at: null, state: 'idle', paused_reason: '', last_run_at: null, last_result: '', last_reason: '', next_run_at: null, not_before: null, round_id: null, completed_attempts: 0, ...patch })
const attempt = (patch: Partial<IQAttempt>): IQAttempt => ({ attempt_no: 1, effort: 'low', challenge: null, expected: '', answer: '', reported_model: '', model_class: '', status: 'smart', reason: '', latency_ms: 1250, diagnostic: null, ...patch })
const round = (id: number, patch: Partial<IQRound> = {}): IQRound => ({ id, model: 'gpt-6-astra', outbound_model: 'gpt-6-astra', mode: 'normal', effort: 'low', status: 'smart', reason: 'correct_answer', started_at: `start-${id}`, finished_at: 'done', prompt_version: 'candy-v3', attempts: [attempt({})], ...patch })
const mountModal = (account = { id: 8, name: 'fixture' } as Account) => mount(IQCheckResultsModal, { props: { show: true, account }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
const runButton = (wrapper: ReturnType<typeof mountModal>) => wrapper.get('[data-testid="iq-run-target"]')

describe('IQ results', () => {
  enableAutoUnmount(afterEach)
  beforeEach(() => {
    vi.clearAllMocks()
    targetsAPI.mockResolvedValue([target('gpt-6.1-sol'), target('gpt-6-astra', { verdict: 'degraded', gate_active: true, gate_model: 'gpt-6-astra-mapped', gate_reset_at: '2026-10-27T08:05:00Z' })])
    roundsAPI.mockResolvedValue({ rounds: [], next_before_id: null })
  })
  afterEach(() => { vi.useRealTimers() })

  it('answers whether the gated model is usable, why, and when the recheck finishes', async () => {
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.findAll('[role="tab"]').map(tab => tab.text())).toEqual(['Astra', 'Sol'])
    const summary = wrapper.get('[data-testid="iq-target-summary"]')
    expect(summary.text()).toContain('admin.accounts.iqGateActive')
    expect(summary.get('[data-testid="iq-verdict-explain"]').text()).toContain('admin.accounts.iqExplain.gate')
    expect(summary.text()).toContain('2026-10-27T08:05:00Z')
    expect(summary.get('[data-testid="iq-gate-model"]').text()).toBe('admin.accounts.iqGateModel{"model":"gpt-6-astra-mapped"}')
    expect(roundsAPI).toHaveBeenCalledWith(8, { model: 'gpt-6-astra', before_id: null }, expect.any(AbortSignal))
    expect(wrapper.emitted('updated')?.[0][0]).toHaveLength(2)
    expect(wrapper.text()).toContain('iqEmpty')
  })

  it('switches targets, loads that model and ignores the previous model late response', async () => {
    let resolveAstra!: (page: unknown) => void
    roundsAPI.mockImplementationOnce(() => new Promise(r => { resolveAstra = r }))
    roundsAPI.mockResolvedValueOnce({ rounds: [round(7, { model: 'gpt-6.1-sol', outbound_model: 'gpt-6.1-sol' })], next_before_id: 3 })
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="iq-tab-gpt-6.1-sol"]').trigger('click')
    await flushPromises()
    resolveAstra({ rounds: [round(9, { reason: 'astra-late' })], next_before_id: null }); await flushPromises()
    expect(wrapper.get('[data-testid="iq-tab-gpt-6.1-sol"]').attributes('aria-selected')).toBe('true')
    expect(roundsAPI).toHaveBeenLastCalledWith(8, { model: 'gpt-6.1-sol', before_id: null }, expect.any(AbortSignal))
    expect(wrapper.findAll('[data-testid="iq-round"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('astra-late')
    roundsAPI.mockResolvedValueOnce({ rounds: [round(2, { model: 'gpt-6.1-sol' })], next_before_id: null })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.iqLoadOlder')!.trigger('click'); await flushPromises()
    expect(roundsAPI).toHaveBeenLastCalledWith(8, { model: 'gpt-6.1-sol', before_id: 3 }, expect.any(AbortSignal))
    expect(wrapper.findAll('[data-testid="iq-round"]')).toHaveLength(2)
  })

  it('shows each question with its table, derivation, answer and self-reported model class', async () => {
    const challenge = { v: 1 as const, r: [7, 9, 8] as [number, number, number], s: [7, 6, 4] as [number, number, number], cols: [2, 0, 1] as [number, number, number], rows: [1, 0] as [number, number], draw: [16, 5] as [number, number], naive: 29 }
    roundsAPI.mockResolvedValue({ rounds: [round(5, { status: 'degraded', reason: 'wrong_answer', attempts: [
      attempt({ attempt_no: 1, challenge, expected: '21', answer: '29', status: 'degraded', reason: 'wrong_answer', reported_model: 'gpt-6-astra', model_class: 'same', diagnostic: { input_tokens: 243, output_tokens: 231, request_id: 'req-1', unexpected_secret: 'must-not-render' } }),
      attempt({ attempt_no: 2, challenge, expected: '21', answer: '21', status: 'degraded', reason: 'model_mismatch', reported_model: 'gpt-5.6-luna', model_class: 'downgrade', diagnostic: { model_identity: { source: 'header', body: ['gpt-6-astra'], header: ['gpt-5.6-luna'], flags: ['source_disagreement'] } } }),
      attempt({ attempt_no: 3, expected: '21', answer: '21' })
    ] })], next_before_id: null })
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.find('[data-testid="iq-attempts"]').exists()).toBe(false)
    await wrapper.get('[data-testid="iq-round"] button').trigger('click'); await flushPromises()
    const [naive, downgrade, legacy] = wrapper.findAll('[data-testid="iq-attempt"]')
    const table = naive.get('[data-testid="iq-challenge"]')
    expect(table.findAll('thead th').slice(1).map(th => th.text())).toEqual(['admin.accounts.iqFlavors.watermelon', 'admin.accounts.iqFlavors.apple', 'admin.accounts.iqFlavors.peach'])
    expect(table.findAll('tbody tr').map(tr => tr.findAll('td').map(td => td.text()))).toEqual([['4', '7', '6'], ['8', '7', '9']])
    expect(naive.get('[data-testid="iq-derivation"]').text()).toBe('admin.accounts.iqDerivation{"answer":"21","circle":16,"star":5}')
    expect(naive.get('[data-testid="iq-outcome"]').text()).toBe('admin.accounts.iqOutcomes.naive')
    expect(naive.get('[data-testid="iq-answer"]').text()).toBe('29')
    expect(naive.text()).toContain('1.25 s')
    expect(naive.text()).toContain('admin.accounts.iqInputTokensValue')
    expect(naive.get('[data-testid="iq-model-class"]').classes()).toContain('badge-gray')
    expect(naive.get('details').attributes('open')).toBeUndefined()
    expect(naive.text()).not.toContain('must-not-render')
    expect(downgrade.get('[data-testid="iq-outcome"]').text()).toBe('admin.accounts.iqOutcomes.downgrade')
    expect(downgrade.get('[data-testid="iq-model-class"]').classes()).toContain('badge-danger')
    expect(downgrade.get('[data-testid="iq-reported-model"]').text()).toMatch(/body.*gpt-6-astra.*header.*gpt-5\.6-luna.*iqSourceDisagreement/)
    expect(legacy.find('[data-testid="iq-challenge"]').exists()).toBe(false)
    expect(legacy.find('[data-testid="iq-model-class"]').exists()).toBe(false)
    expect(legacy.get('[data-testid="iq-derivation"]').text()).toBe('21')
  })

  it('disables manual checks while queued or running, polls every 3 seconds and stops on close', async () => {
    vi.useFakeTimers()
    let resolveRun!: (data: unknown) => void
    run.mockImplementationOnce(() => new Promise(r => { resolveRun = r }))
    const wrapper = mountModal()
    await flushPromises()
    expect(runButton(wrapper).attributes('disabled')).toBeUndefined()
    await runButton(wrapper).trigger('click')
    await runButton(wrapper).trigger('click')
    expect(run).toHaveBeenCalledTimes(1)
    expect(run).toHaveBeenCalledWith(8, ['gpt-6-astra'])
    expect(runButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="iq-run-all"]').attributes('disabled')).toBeDefined()
    targetsAPI.mockResolvedValue([target('gpt-6.1-sol'), target('gpt-6-astra', { verdict: 'degraded', gate_active: true, gate_model: 'gpt-6-astra', state: 'running', completed_attempts: 2 })])
    resolveRun([{ model: 'gpt-6-astra', state: 'queued', round_id: 4 }])
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.iqRunAccepted')
    expect(runButton(wrapper).attributes('disabled')).toBeDefined()
    expect(runButton(wrapper).text()).toBe('admin.accounts.iqStates.running')
    expect(wrapper.get('[data-testid="iq-target-summary"]').text()).toContain('admin.accounts.iqRunningProgress{"count":2}')
    expect(wrapper.find('[data-testid="iq-gate-model"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="iq-run-all"]').attributes('disabled')).toBeUndefined()
    const calls = targetsAPI.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(targetsAPI.mock.calls.length).toBe(calls + 1)
    await wrapper.setProps({ show: false })
    await vi.advanceTimersByTimeAsync(10000)
    expect(targetsAPI.mock.calls.length).toBe(calls + 1)
  })

  it('reports rejected targets of a full check', async () => {
    run.mockResolvedValueOnce([{ model: 'gpt-6-astra', state: 'queued' }, { model: 'gpt-6.1-sol', state: 'rejected', reason: 'credentials_unavailable' }])
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="iq-run-all"]').trigger('click'); await flushPromises()
    expect(run).toHaveBeenCalledWith(8, undefined)
    expect(wrapper.get('[role="alert"]').text()).toContain('Sol: admin.accounts.iqReasons.credentials_unavailable')
  })

  it('discards late responses after changing accounts and keeps rounds when a download fails', async () => {
    let resolve!: (targets: unknown) => void
    targetsAPI.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    roundsAPI.mockResolvedValue({ rounds: [round(1)], next_before_id: null })
    vi.mocked(downloadIQCheckDiagnostics).mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountModal()
    await wrapper.setProps({ account: { id: 9 } as Account })
    await flushPromises()
    resolve([target('old-account-model')]); await flushPromises()
    expect(wrapper.text()).not.toContain('old-account-model')
    expect(wrapper.emitted('updated')).toHaveLength(1)
    await wrapper.findAll('button').find(button => button.text().includes('iqDownload'))!.trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-testid="iq-round"]')).toHaveLength(1)
    expect(wrapper.get('[role="alert"]').text()).toContain('iqDownloadFailed')
  })
})
