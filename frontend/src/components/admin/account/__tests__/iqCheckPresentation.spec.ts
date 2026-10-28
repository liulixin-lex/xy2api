import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
import zh from '@/i18n/locales/zh/admin/accounts'
import en from '@/i18n/locales/en/admin/accounts'
import { iqEffortLabel, iqRunSummary, iqTechnicalGroups, iqTokenSummary } from '../iqCheckPresentation'
import type { IQDiagnostic } from '@/types'

const diagnostic: IQDiagnostic = { parser_version: 'iq-response-v7', stage: 'grade', code: 'correct_answer', protocol: 'responses', http_status: 200, first_byte_ms: 1891, total_ms: 10882, input_tokens: 243, output_tokens: 231, reasoning_tokens: 220, media_type: 'text/event-stream', retry_visibility: 'single_attempt', answer_source: 'terminal_output', request_id: 'example-id', event_index: 15 }
describe('IQ presentation', () => {
 it.each(['zh', 'en'])('identifies the precise size limit in %s', locale => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } })
  const rows = iqTechnicalGroups({ parser_version: 'iq-response-v5', stage: 'decode', code: 'response_too_large', category: 'protocol', limit_kind: 'response_bytes', limit_bytes: 8388608, bytes_read: 8388609 }, i18n.global.t, i18n.global.te).flatMap(group => group.rows)
  expect(rows.find(row => row.raw === 'response_bytes')?.value).not.toBe('response_bytes')
  expect(rows.find(row => row.raw === '8388608')?.value).toBe('8,388,608 B')
  expect(rows.find(row => row.raw === 'protocol')?.value).not.toBe('protocol')
 })
 it('summarizes tokens with reasoning included in output', () => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale: 'zh', messages: { zh: { admin: zh } } })
  expect(iqTokenSummary(diagnostic, i18n.global.t)).toBe('输入 243 · 输出 231（含推理 220）')
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
 it.each(['zh', 'en'])('retains raw technical codes while translating %s labels', locale => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } })
  const rows = iqTechnicalGroups(diagnostic, i18n.global.t, i18n.global.te).flatMap(group => group.rows)
  expect(rows.some(row => row.raw === 'terminal_output')).toBe(true)
  expect(rows.some(row => row.raw === 'correct_answer')).toBe(false)
  expect(rows.some(row => row.raw === 'single_attempt')).toBe(true)
  expect(rows.some(row => row.raw === 'example-id')).toBe(true)
  expect(rows.find(row => row.raw === '1891')?.value).toBe('1.89 s')
 })
 it('does not invent missing metadata, skips unknown keys and preserves unknown enum values', () => {
  const t = (key: string) => key
  expect(iqTechnicalGroups(null, t, () => false)).toEqual([])
  expect(iqEffortLabel('high')).toBe('high'); expect(iqEffortLabel('upstream_default')).toBe('default')
  const rows = iqTechnicalGroups({ parser_version: 'future', stage: 'new-stage', unexpected_secret: 'must-not-render' }, t, () => false).flatMap(group => group.rows)
  expect(rows.find(row => row.raw === 'new-stage')?.value).toBe('new-stage')
  expect(rows.some(row => row.raw === 'must-not-render')).toBe(false)
 })
})
