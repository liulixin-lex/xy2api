import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// Keys built at runtime (`sections.${key}`, `reasons.${reason}`) escape the
// static locale completeness check, so the 0.4.4 ones are listed here.
const dynamicKeys = [
  ...['incremental_declarations', 'records', 'tree_affinity', 'ladder', 'lossless_first', 'record_routing', 'strict_marker_veto', 'local_ending']
    .flatMap((key) => ['admin.resilience.settings.collab.' + key, 'admin.resilience.settings.collab.' + key + 'Hint']),
  ...['off', 'groups', 'all'].map((key) => 'admin.resilience.settings.collab.modes.' + key),
  ...['visible', 'moderation', 'permanent'].map((key) => 'admin.resilience.settings.collab.consent.' + key),
  ...['failure', 'stream', 'capability'].flatMap((k) => [`admin.resilience.settings.sections.${k}`, `admin.resilience.settings.sections.${k}Hint`]),
  ...['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal', 'failedFrameRetryAfter']
    .flatMap((k) => [`admin.resilience.settings.switches.${k}`, `admin.resilience.settings.switches.${k}Hint`]),
  ...['balancePause', 'codexRetryAfterCap', 'learnTtl'].flatMap((k) => [`admin.resilience.settings.fields.${k}`, `admin.resilience.settings.fields.${k}Hint`]),
  'admin.resilience.explain.reasons.capability_stream_only',
  'admin.resilience.panel.metrics.pre_created_delta',
  ...['collab_plaintext', 'collab_call', 'collab_call_action', 'state_rejection'].map((k) => `admin.resilience.panel.metrics.${k}`),
  // Round 3: the session_rebind counter is the request's avoidance; its hint says where the committed switch is.
  'admin.resilience.panel.metricHints.session_rebind',
  // Round 3 browser check (#9): the request budget counters the backend emits had no copy and showed raw names.
  ...['budget_snapshot', 'budget_cut_fot', 'ttfo_lower_bound', 'late_attempt_start', 'body_receipt_repeat', 'anthropic_response_stage_timeout']
    .flatMap((k) => [`admin.resilience.panel.metrics.${k}`, `admin.resilience.panel.metricHints.${k}`])
]

function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined), messages)
}

describe('gateway resilience 0.4.4 locale keys', () => {
  it.each([
    ['zh', zh, '默认 5/15/60 分钟，第 3 档封顶'],
    ['en', en, '5/15/60 min by default; the third step is the cap']
  ])('%s has every runtime-built key and describes the default balance pause schedule', (_, messages, defaultSchedule) => {
    const missing = dynamicKeys.filter((key) => typeof lookup(messages, key) !== 'string' || !(lookup(messages, key) as string).trim())
    expect(missing).toEqual([])
    expect(lookup(messages, 'admin.resilience.settings.fields.balancePauseHint')).toContain(defaultSchedule)
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


// final-fe reviews (gpt item 1, Astra items 1-4): the budget counter labels say what the backend counts
// (body_receipt.go, request_timing.go, budget.go, timeout_plan.go, resilience_timeout_observe.go): repeats only,
// lateness from request arrival, the configured start mode, the attempt's wait lower bound, and would (the budget
// limited the planned window) apart from applied (the budget-cut attribution took effect).
describe.each([
  ['zh', zh, {
    body_receipt_repeat: ['重复接收次数'], late_attempt_start: ['请求到达时间'], budget_snapshot: ['配置的预算起算模式'],
    ttfo_lower_bound: ['等待下界'], budget_cut_fot: ['记录到预算限制', '归因']
  }],
  ['en', en, {
    body_receipt_repeat: ['Repeat request-body receipts'], late_attempt_start: ['request arrival'], budget_snapshot: ['configured budget start mode'],
    ttfo_lower_bound: ['lower bound'], budget_cut_fot: ['budget limit recorded', 'attribution applied']
  }]
] as const)('%s budget counter labels', (_, messages, expected) => {
  it.each(Object.entries(expected))('%s says %j', (name, phrases) => {
    for (const phrase of phrases) expect(lookup(messages, `admin.resilience.panel.metrics.${name}`)).toContain(phrase)
  })
  // final-fe Astra re-review 1: with attribution off both flags read false although the budget may still cut the window.
  it('says in the budget_cut_fot tip that attribution off leaves both flags false', () => {
    expect(lookup(messages, 'admin.resilience.panel.metricHints.budget_cut_fot')).toMatch(/归因模式为 off 时两个标记都不计算|With attribution off neither flag is computed/)
  })
  it('never reads the repeat count as "only the first counts"', () => {
    expect(lookup(messages, 'admin.resilience.panel.metrics.body_receipt_repeat')).not.toMatch(/只按第一次计|only the first counts/)
  })
})


describe('collaboration recovery risk copy', () => {
  it.each([
    ['en', en, 'Try to preserve content first', 'lease', 'Recovery ladder'],
    ['zh', zh, '优先保留内容', '租约', '恢复阶梯']
  ])('%s explains borrowing and migration dependencies', (_, messages, lossless, lease, ladder) => {
    const text = (key: string) => lookup(messages, 'admin.resilience.settings.collab.' + key) as string
    expect(text('record_routingHint')).toContain(lossless)
    expect(text('record_routingHint')).toContain(ladder)
    expect(text('tree_affinityHint')).toContain(lossless)
    expect(text('tree_affinityHint')).toContain(lease)
    expect(text('lossless_firstHint')).toContain(lease)
  })

  it.each([
    ['en', en, 'can add errors', 'not recommended', 'existing repair first'],
    ['zh', zh, '增加报错', '不建议开启', '先原样走完现有修复']
  ])('%s explains the two risky switches and default repair order', (_, messages, risk, recommendation, order) => {
    const text = (key: string) => lookup(messages, 'admin.resilience.settings.collab.' + key) as string
    expect(text('lossless_firstHint')).toContain(risk)
    expect(text('strict_marker_vetoHint')).toContain(risk)
    expect(text('strict_marker_vetoHint')).toContain(recommendation)
    expect(text('ladderHint')).toContain(order)
  })
})
