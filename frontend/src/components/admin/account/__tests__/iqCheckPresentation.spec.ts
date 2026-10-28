import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
import zh from '@/i18n/locales/zh/admin/accounts'
import en from '@/i18n/locales/en/admin/accounts'
import { iqEffortLabel, iqRunSummary, iqTechnicalRows, iqTokenSummary } from '../iqCheckPresentation'
import type { IQAttempt, IQDiagnostic } from '@/types'

const attempt = (status: IQAttempt['status'], diagnostic: IQDiagnostic | null, reason = ''): IQAttempt => ({ attempt_no: 1, effort: 'low', challenge: null, expected: '21', answer: '', reported_model: '', model_class: '', status, reason, latency_ms: null, diagnostic })
const i18nFor = (locale: string) => createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } }).global
const rows = (a: IQAttempt, locale = 'zh') => { const g = i18nFor(locale); return Object.fromEntries(iqTechnicalRows(a, g.t, g.te, value => `at ${value}`).map(row => [row.key, row.value])) }
describe('IQ presentation', () => {
 it('summarizes tokens with reasoning included in output', () => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale: 'zh', messages: { zh: { admin: zh } } })
  expect(iqTokenSummary({ input_tokens: 243, output_tokens: 231, reasoning_tokens: 220 }, i18n.global.t)).toBe('输入 243 · 输出 231（含推理 220）')
  expect(iqTokenSummary(null, i18n.global.t)).toBe('')
 })
 it('counts run results per target and whole rejected accounts once', () => {
  expect(iqRunSummary([{ model: 'gpt-6-astra', state: 'queued' }, { model: 'gpt-6.1-sol', state: 'rejected', reason: 'not_openai' }])).toEqual({ queued: 1, rejected: 1, reasons: ['not_openai'] })
  expect(iqRunSummary([
   { account_id: 1, targets: [{ model: 'gpt-6-astra', state: 'running' }, { model: 'custom', state: 'rejected', reason: 'unknown_target' }] },
   { account_id: 2, reason: 'not_found', targets: [] },
   { account_id: 3, reason: 'not_openai', targets: [] }
  ])).toEqual({ queued: 1, rejected: 3, reasons: ['not_found', 'not_openai', 'unknown_target'] })
 })
 it('shows request ID, timing and tokens for the compact diagnostic of a correct answer', () => {
  expect(rows(attempt('smart', { request_id: 'req-1', http_status: 200, first_byte_ms: 11670, total_ms: 16200, input_tokens: 243, output_tokens: 241, reasoning_tokens: 230 }))).toEqual({
   request_id: 'req-1', latency: '首字节 11.67 s / 总计 16.20 s', tokens: '输入 243 · 输出 241（含推理 230）'
  })
 })
 it.each(['zh', 'en'])('keeps only failure facts from a historical full diagnostic in %s', locale => {
  const full = { parser_version: 'iq-response-v7', stage: 'parse', code: 'empty_answer', protocol: 'responses', transport: 'http', media_type: 'text/event-stream', answer_source: 'terminal_output', category: 'answer', retry_visibility: 'single_attempt', limit_kind: 'response_bytes', bytes_read: 10, http_status: 429, error_code: 'rate_limit_exceeded', error_type: 'requests', retry_after_unbounded: true, first_byte_ms: 0, total_ms: 820 }
  const shown = rows(attempt('unknown', full), locale)
  expect(Object.keys(shown)).toEqual(['latency', 'http_status', 'error', 'retry_after', 'failure'])
  expect(shown.http_status).toBe('429')
  expect(shown.error).toBe('rate_limit_exceeded · requests')
  expect(shown.retry_after).not.toBe('true')
  expect(shown.failure).not.toMatch(/parse|empty_answer/)
  expect(rows(attempt('unknown', { retry_after: '2026-10-28T06:00:00Z' }), locale).retry_after).toBe('at 2026-10-28T06:00:00Z')
 })
 it('locates a failure from the attempt reason when the diagnostic has no code, and only for unknown answers', () => {
  expect(rows(attempt('unknown', null, 'timeout'))).toEqual({ failure: '请求超时' })
  expect(rows(attempt('degraded', { stage: 'grade', code: 'wrong_answer' }, 'wrong_answer'))).toEqual({})
  expect(rows(attempt('unknown', { stage: 'new-stage', code: 'new-code' })).failure).toBe('new-stage · new-code')
  expect(iqEffortLabel('high')).toBe('high'); expect(iqEffortLabel('upstream_default')).toBe('default')
 })
})
