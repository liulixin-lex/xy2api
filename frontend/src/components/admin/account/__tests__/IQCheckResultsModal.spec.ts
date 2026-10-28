import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import IQCheckResultsModal from '../IQCheckResultsModal.vue'
import type { Account, IQAttempt, IQRound, IQRoundPage, IQTarget } from '@/types'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
vi.mock('@/api/admin/accounts', () => ({ downloadIQCheckDiagnostics: vi.fn() }))
const { targetsAPI, roundsAPI, run } = vi.hoisted(() => ({ targetsAPI: vi.fn(), roundsAPI: vi.fn(), run: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getIQCheck: targetsAPI, listIQResults: roundsAPI, runIQCheck: run } } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (s: string, p?: Record<string, unknown>) => p ? `${s}${JSON.stringify(p)}` : s, te: () => true }) }))

const target = (model: string, patch: Partial<IQTarget> = {}): IQTarget => ({ model, enabled: true, reasoning_effort: 'low', interval_minutes: 5, timeout_seconds: 120, verdict: 'smart', verdict_at: null, gate_active: false, gate_model: null, gate_reset_at: null, state: 'idle', paused_reason: '', last_run_at: null, last_result: '', last_reason: '', next_run_at: null, not_before: null, round_id: null, completed_attempts: 0, ...patch })
const attempt = (patch: Partial<IQAttempt>): IQAttempt => ({ attempt_no: 1, effort: 'low', challenge: null, expected: '', answer: '', reported_model: '', model_class: '', status: 'smart', reason: '', latency_ms: 1250, diagnostic: null, ...patch })
const round = (id: number, patch: Partial<IQRound> = {}): IQRound => ({ id, model: 'gpt-6-astra', outbound_model: 'gpt-6-astra', mode: 'normal', effort: 'low', status: 'smart', reason: 'correct_answer', started_at: `start-${id}`, finished_at: 'done', prompt_version: 'candy-v3', attempts: [attempt({})], ...patch })
const pageOf = (rounds: IQRound[], total = rounds.length, page = 1): IQRoundPage => ({ rounds, total, page, page_size: 10 })
const query = (patch: Record<string, unknown> = {}) => ({ model: 'gpt-6-astra', page: 1, page_size: 10, filter: '', ...patch })
const mountModal = (account = { id: 8, name: 'fixture' } as Account) => mount(IQCheckResultsModal, { props: { show: true, account }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
type Wrapper = ReturnType<typeof mountModal>
const runButton = (wrapper: Wrapper) => wrapper.get('[data-testid="iq-run-target"]')
const roundIds = (wrapper: Wrapper) => wrapper.findAll('[data-testid="iq-round"] time').map(time => time.text())
const goTo = (wrapper: Wrapper, page: number) => wrapper.get(`[aria-label='pagination.goToPage{"page":${page}}']`).trigger('click')
const deferred = () => { let resolve!: (page: IQRoundPage) => void; const promise = new Promise<IQRoundPage>(r => { resolve = r }); return { promise, resolve } }

describe('IQ results', () => {
  enableAutoUnmount(afterEach)
  beforeEach(() => {
    vi.clearAllMocks()
    targetsAPI.mockResolvedValue([target('gpt-6.1-sol'), target('gpt-6-astra', { verdict: 'degraded', gate_active: true, gate_model: 'gpt-6-astra-mapped', gate_reset_at: '2026-10-27T08:05:00Z' })])
    roundsAPI.mockResolvedValue(pageOf([]))
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
    expect(roundsAPI).toHaveBeenCalledWith(8, query(), expect.any(AbortSignal))
    expect(wrapper.emitted('updated')?.[0][0]).toHaveLength(2)
    expect(wrapper.text()).toContain('iqEmpty')
  })

  it('shows awaiting run for an overdue queued target and the self-resuming reason for an unschedulable account', async () => {
    targetsAPI.mockResolvedValue([target('gpt-6-astra', { state: 'queued', next_run_at: '2020-01-01T00:00:00Z' }), target('gpt-6.1-sol', { state: 'paused', paused_reason: 'account_unschedulable', next_run_at: '2020-01-01T00:00:00Z' })])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[data-testid="iq-next"]').text()).toBe('admin.accounts.iqAwaitingRun')
    expect(wrapper.find('[data-testid="iq-paused"]').exists()).toBe(false)
    await wrapper.get('[data-testid="iq-tab-gpt-6.1-sol"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="iq-next"]').text()).toBe('—')
    expect(wrapper.get('[data-testid="iq-paused"]').text()).toBe('admin.accounts.iqReasons.account_unschedulable')
  })

  it('switches targets, loads that model and ignores the previous model late response', async () => {
    const astra = deferred()
    roundsAPI.mockImplementationOnce(() => astra.promise)
    roundsAPI.mockResolvedValueOnce(pageOf([round(7, { model: 'gpt-6.1-sol', outbound_model: 'gpt-6.1-sol' })]))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="iq-tab-gpt-6.1-sol"]').trigger('click')
    await flushPromises()
    astra.resolve(pageOf([round(9, { reason: 'astra-late' })])); await flushPromises()
    expect(wrapper.get('[data-testid="iq-tab-gpt-6.1-sol"]').attributes('aria-selected')).toBe('true')
    expect(roundsAPI).toHaveBeenLastCalledWith(8, query({ model: 'gpt-6.1-sol' }), expect.any(AbortSignal))
    expect(roundIds(wrapper)).toEqual(['start-7'])
    expect(wrapper.text()).not.toContain('astra-late')
  })

  it('pages and filters, returning to page 1 on a filter change and dropping responses that arrive late', async () => {
    roundsAPI.mockResolvedValue(pageOf([round(30), round(29)], 25))
    const wrapper = mountModal()
    await flushPromises()
    const late = deferred()
    roundsAPI.mockImplementationOnce(() => late.promise)
    await goTo(wrapper, 2)
    expect(roundsAPI).toHaveBeenLastCalledWith(8, query({ page: 2 }), expect.any(AbortSignal))
    roundsAPI.mockResolvedValueOnce(pageOf([round(12, { status: 'degraded' })], 3))
    await wrapper.get('[data-testid="iq-filter-abnormal"]').trigger('click'); await flushPromises()
    expect(roundsAPI).toHaveBeenLastCalledWith(8, query({ filter: 'abnormal' }), expect.any(AbortSignal))
    late.resolve(pageOf([round(20), round(19)], 25, 2)); await flushPromises()
    expect(roundIds(wrapper)).toEqual(['start-12'])
    expect(wrapper.get('[data-testid="iq-filter-abnormal"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find(`[aria-label='pagination.goToPage{"page":2}']`).exists()).toBe(false)
  })

  it('replaces page 1 while polling, freezes older pages and stops once every target is idle', async () => {
    vi.useFakeTimers()
    targetsAPI.mockResolvedValue([target('gpt-6-astra', { state: 'running', round_id: 31 })])
    roundsAPI.mockResolvedValueOnce(pageOf([round(30), round(29)], 12))
    const wrapper = mountModal()
    await flushPromises()
    roundsAPI.mockResolvedValueOnce(pageOf([round(31, { finished_at: null }), round(30)], 13))
    await vi.advanceTimersByTimeAsync(3000)
    expect(roundIds(wrapper)).toEqual(['start-31', 'start-30'])

    roundsAPI.mockResolvedValueOnce(pageOf([round(20)], 13, 2))
    await goTo(wrapper, 2); await flushPromises()
    const listCalls = roundsAPI.mock.calls.length, targetCalls = targetsAPI.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(targetsAPI.mock.calls.length).toBe(targetCalls + 1)
    expect(roundsAPI.mock.calls.length).toBe(listCalls)
    expect(roundIds(wrapper)).toEqual(['start-20'])

    targetsAPI.mockResolvedValue([target('gpt-6-astra')])
    await vi.advanceTimersByTimeAsync(3000)
    await vi.advanceTimersByTimeAsync(10000)
    expect(targetsAPI.mock.calls.length).toBe(targetCalls + 2)
  })

  it('keeps one round open at a time', async () => {
    roundsAPI.mockResolvedValue(pageOf([round(2), round(1)]))
    const wrapper = mountModal()
    await flushPromises()
    const [first, second] = wrapper.findAll('[data-testid="iq-round"] > button')
    await first.trigger('click'); await flushPromises()
    await second.trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-testid="iq-attempts"]')).toHaveLength(1)
    expect(second.attributes('aria-expanded')).toBe('true')
    expect(first.attributes('aria-expanded')).toBe('false')
  })

  it('aligns each question as correct answer, model answer, naive answer and self-reported model', async () => {
    const challenge = { v: 1 as const, r: [7, 9, 8] as [number, number, number], s: [7, 6, 4] as [number, number, number], cols: [2, 0, 1] as [number, number, number], rows: [1, 0] as [number, number], draw: [16, 5] as [number, number], naive: 29 }
    roundsAPI.mockResolvedValue(pageOf([round(5, { status: 'degraded', reason: 'wrong_answer', attempts: [
      attempt({ attempt_no: 1, challenge, expected: '21', answer: '29', status: 'degraded', reason: 'wrong_answer', reported_model: 'gpt-6-astra', model_class: 'same' }),
      attempt({ attempt_no: 2, challenge, expected: '21', answer: '21', status: 'degraded', reason: 'model_mismatch', reported_model: 'gpt-5.6-luna', model_class: 'downgrade', diagnostic: { model_identity: { source: 'header', body: ['gpt-6-astra'], header: ['gpt-5.6-luna'], flags: ['source_disagreement'] } } }),
      attempt({ attempt_no: 3, expected: '21', status: 'unknown', reason: 'empty_answer' })
    ] })]))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="iq-round"] button').trigger('click'); await flushPromises()
    const [naive, downgrade, legacy] = wrapper.findAll('[data-testid="iq-attempt"]')
    const table = naive.get('[data-testid="iq-challenge"]')
    expect(table.findAll('thead th').slice(1).map(th => th.text())).toEqual(['admin.accounts.iqFlavors.watermelon', 'admin.accounts.iqFlavors.apple', 'admin.accounts.iqFlavors.peach'])
    expect(table.findAll('tbody tr').map(tr => tr.findAll('td').map(td => td.text()))).toEqual([['4', '7', '6'], ['8', '7', '9']])
    expect(naive.findAll('[data-testid="iq-answers"] dt').map(dt => dt.text())).toEqual(['admin.accounts.iqExpected', 'admin.accounts.iqNormalizedAnswer', 'admin.accounts.iqNaive', 'admin.accounts.iqReportedModel'])
    expect(naive.findAll('[data-testid="iq-expected"] span').map(span => span.text())).toEqual(['21', 'admin.accounts.iqDerivation{"circle":16,"star":5}'])
    expect(naive.findAll('[data-testid="iq-answer"] span').map(span => span.text())).toEqual(['29', 'admin.accounts.iqNaiveMatch'])
    expect(naive.get('[data-testid="iq-answer"] span').classes()).toContain('text-red-700')
    expect(naive.get('[data-testid="iq-outcome"]').text()).toBe('admin.accounts.iqOutcomes.naive')
    expect(downgrade.get('[data-testid="iq-answer"]').text()).toBe('21')
    expect(downgrade.get('[data-testid="iq-model-class"]').classes()).toContain('badge-danger')
    expect(downgrade.get('[data-testid="iq-reported-model"]').text()).toMatch(/body.*gpt-6-astra.*header.*gpt-5\.6-luna.*iqSourceDisagreement/)
    expect(legacy.findAll('[data-testid="iq-answers"] dt').map(dt => dt.text())).toEqual(['admin.accounts.iqExpected', 'admin.accounts.iqNormalizedAnswer', 'admin.accounts.iqReportedModel'])
    expect(legacy.get('[data-testid="iq-answer"]').text()).toBe('admin.accounts.iqReasons.empty_answer')
  })

  it('shows only the kept technical details, without raw codes, for compact and full diagnostics', async () => {
    const full = { parser_version: 'iq-response-v7', stage: 'parse', code: 'empty_answer', protocol: 'responses', media_type: 'text/event-stream', answer_source: 'terminal_output', category: 'answer', http_status: 403, error_code: 'blocked', error_type: 'forbidden', retry_after: '2026-10-28T06:00:00Z', first_byte_ms: 0, total_ms: 820, request_id: 'req-full' }
    roundsAPI.mockResolvedValue(pageOf([round(5, { attempts: [
      attempt({ attempt_no: 1, expected: '21', answer: '21', diagnostic: { request_id: 'req-1', http_status: 200, first_byte_ms: 11670, total_ms: 16200, input_tokens: 243, output_tokens: 241, reasoning_tokens: 230 } }),
      attempt({ attempt_no: 2, expected: '21', status: 'unknown', reason: 'empty_answer', diagnostic: full })
    ] })]))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="iq-round"] button').trigger('click'); await flushPromises()
    const [compact, failed] = wrapper.findAll('[data-testid="iq-technical-details"]')
    expect(compact.attributes('open')).toBeUndefined()
    expect(compact.findAll('dt').map(dt => dt.text())).toEqual(['request_id', 'latency', 'tokens'].map(key => 'admin.accounts.iqDiagnosticLabels.' + key))
    expect(compact.get('[data-testid="iq-tech-latency"]').text()).toBe('admin.accounts.iqFirstByteValue{"value":"11.67 s"} / admin.accounts.iqTotalValue{"value":"16.20 s"}')
    expect(failed.findAll('dt').map(dt => dt.text())).toEqual(['request_id', 'latency', 'http_status', 'error', 'retry_after', 'failure'].map(key => 'admin.accounts.iqDiagnosticLabels.' + key))
    expect(failed.get('[data-testid="iq-tech-failure"]').text()).toBe('admin.accounts.iqStages.parse · admin.accounts.iqReasons.empty_answer')
    for (const raw of ['iq-response-v7', 'responses', 'text/event-stream', 'terminal_output', 'answer']) expect(failed.findAll('dd').map(dd => dd.text())).not.toContain(raw)
    expect(failed.text()).not.toMatch(/iq-response-v7|event-stream|terminal_output/)
  })

  it('disables manual checks while queued or running and stops polling on close', async () => {
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
    roundsAPI.mockResolvedValue(pageOf([round(1)]))
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
