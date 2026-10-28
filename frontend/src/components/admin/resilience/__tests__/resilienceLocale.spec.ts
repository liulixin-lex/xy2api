import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// Keys built at runtime (`sections.${key}`, `reasons.${reason}`) escape the
// static locale completeness check, so the 0.4.4 ones are listed here.
const dynamicKeys = [
  ...['failure', 'stream', 'capability'].flatMap((k) => [`admin.resilience.settings.sections.${k}`, `admin.resilience.settings.sections.${k}Hint`]),
  ...['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal', 'failedFrameRetryAfter']
    .flatMap((k) => [`admin.resilience.settings.switches.${k}`, `admin.resilience.settings.switches.${k}Hint`]),
  ...['balancePause', 'codexRetryAfterCap', 'learnTtl'].flatMap((k) => [`admin.resilience.settings.fields.${k}`, `admin.resilience.settings.fields.${k}Hint`]),
  'admin.resilience.explain.reasons.capability_stream_only',
  'admin.resilience.panel.metrics.pre_created_delta'
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
  // added events; since 2026-10-22 also the exact response.metadata event;
  // since 0.4.9 also codex.rate_limits, but no other codex.* metadata. The
  // Codex cap also covers configuration rejections.
  it.each([
    ['zh', zh, { queued: 'response.queued', added: 'added', configReject: '配置拒绝' }],
    ['en', en, { queued: 'response.queued', added: 'added', configReject: 'configuration rejection' }]
  ])('%s describes what the whitelist and the Codex cap actually cover', (_, messages, words) => {
    const text = (key: string) => lookup(messages, `admin.resilience.settings.${key}`) as string
    const whitelist = text('switches.nativePreludeWhitelistHint')
    expect(whitelist).toContain(words.queued)
    expect(whitelist).toContain(words.added)
    expect(whitelist).toContain('response.metadata')
    expect(whitelist.split('codex.rate_limits').join('')).not.toContain('codex.')
    expect(text('fields.codexRetryAfterCapHint')).toContain(words.configReject)
  })

  // PM 3 (resilience_relay_keepalive.go): the generalized empty-delta drop
  // follows the whitelist switch too, so "off" names the marked-frame rule.
  it.each([['zh', zh, '只丢弃带 SSE-Keep-Alive 标记的帧'], ['en', en, 'only frames marked SSE-Keep-Alive']])(
    '%s says what is dropped with the whitelist off',
    (_, messages, offRule) => {
      expect(lookup(messages, 'admin.resilience.settings.switches.nativePreludeWhitelistHint')).toContain(offRule)
    }
  )

  // γ a03ad9c79: only requests whose upstream stream follows the client are
  // filtered; requests the gateway already sends as streaming are not.
  it.each([['zh', zh, '会因此失败的非流式请求', '改发流式'], ['en', en, 'non-streaming requests that would fail', 'already sends as streaming']])(
    '%s scopes the stream-only filter to requests that would fail',
    (_, messages, scope, unaffected) => {
      for (const key of ['settings.sections.capabilityHint', 'capability.title']) {
        const text = lookup(messages, `admin.resilience.${key}`) as string
        expect(text).toContain(scope)
        expect(text).toContain(unaffected)
      }
    }
  )

  // γ 29f26652d: turning decision_v2 off restores the old rules, but the
  // richer evidence extraction stays, so some ops categories may differ.
  it.each([['zh', zh, '0.4.3 的分类规则', '运维记录'], ['en', en, '0.4.3 classification rules', 'ops records']])(
    '%s does not promise a byte-identical 0.4.3 classification',
    (_, messages, rules, caveat) => {
      const text = lookup(messages, 'admin.resilience.settings.sections.failureHint') as string
      expect(text).toContain(rules)
      expect(text).toContain(caveat)
    }
  )

  // α P2-11: failures that are not retryable (model not found, input or auth
  // errors, billing service errors) never carry retry-after either.
  it.each([['zh', zh, '不可重试'], ['en', en, 'not retryable']])('%s names non-retryable failures in the failed-frame hint', (_, messages, word) => {
    expect(lookup(messages, 'admin.resilience.settings.switches.failedFrameRetryAfterHint')).toContain(word)
  })
})

