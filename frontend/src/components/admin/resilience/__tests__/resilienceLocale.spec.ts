import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// Keys built at runtime (`sections.${key}`, `reasons.${reason}`) escape the
// static locale completeness check, so the 0.4.4 ones are listed here.
const dynamicKeys = [
  ...['failure', 'stream', 'capability'].flatMap((k) => [`admin.resilience.settings.sections.${k}`, `admin.resilience.settings.sections.${k}Hint`]),
  ...['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal', 'failedFrameRetryAfter']
    .flatMap((k) => [`admin.resilience.settings.switches.${k}`, `admin.resilience.settings.switches.${k}Hint`]),
  ...['codexRetryAfterCap', 'learnTtl'].flatMap((k) => [`admin.resilience.settings.fields.${k}`, `admin.resilience.settings.fields.${k}Hint`]),
  'admin.resilience.explain.reasons.capability_stream_only'
]

function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined), messages)
}

describe('gateway resilience 0.4.4 locale keys', () => {
  it.each([['zh', zh], ['en', en]])('%s has every runtime-built key', (_, messages) => {
    const missing = dynamicKeys.filter((key) => typeof lookup(messages, key) !== 'string' || !(lookup(messages, key) as string).trim())
    expect(missing).toEqual([])
  })

  // C0: both errors switches also need errors.enabled; moving the current
  // request on a stream-only 400 also needs failure.decision_v2.
  it.each([['zh', zh], ['en', en]])('%s hints name the card each switch depends on', (_, messages) => {
    const text = (key: string) => lookup(messages, `admin.resilience.settings.${key}`) as string
    expect(text('switches.failedFrameRetryAfterHint')).toContain(text('sections.errors'))
    expect(text('fields.codexRetryAfterCapHint')).toContain(text('sections.errors'))
    expect(text('sections.capabilityHint')).toContain(text('sections.failure'))
  })

  // DOCS/PM 2026-10-21: the whitelist adds response.queued and content-free
  // added events, not metadata events; the Codex cap also covers
  // configuration rejections.
  it.each([
    ['zh', zh, { queued: 'response.queued', added: 'added', notMetadata: '元数据', configReject: '配置拒绝' }],
    ['en', en, { queued: 'response.queued', added: 'added', notMetadata: 'metadata', configReject: 'configuration rejection' }]
  ])('%s describes what the whitelist and the Codex cap actually cover', (_, messages, words) => {
    const text = (key: string) => lookup(messages, `admin.resilience.settings.${key}`) as string
    const whitelist = text('switches.nativePreludeWhitelistHint')
    expect(whitelist).toContain(words.queued)
    expect(whitelist).toContain(words.added)
    expect(whitelist).not.toContain(words.notMetadata)
    expect(text('fields.codexRetryAfterCapHint')).toContain(words.configReject)
  })
})

