// Synthetic data for the cache observability panel, used until the P0b admin
// endpoints exist. Everything here is generated: host names use the reserved
// `.example` domain and no value comes from production. The page labels the
// data as synthetic whenever this client is active.
import type {
  BreakdownDimension,
  BreakdownQuery,
  BreakdownResponse,
  BreakdownRow,
  CachePanelClient,
  Experiment,
  ExperimentsResponse,
  Granularity,
  LossClassStat,
  NorthStarPoint,
  NorthStarQuery,
  NorthStarResponse,
  PanelEvent,
  SummaryWindow,
  TraceStatsQuery,
  TraceStatsResponse,
} from './types'

export const MOCK_POLICY_VERSION = '2026-10-22-v1'

const HOUR_MS = 3_600_000
/** Synthetic price gap (miss price minus read price) per token, in USD; not a real price. */
const SYNTHETIC_W = 1.12e-6
const COST_MULTIPLIER: Record<string, number> = { list: 1, charged: 0.42, upstream: 0.18 }
const WINDOW_HOURS: Record<SummaryWindow, number> = { '24h': 24, '7d': 168, '30d': 720 }

/** §8.2 notes for a view: the weighted hit rate stays on list prices; upstream covers only part of the traffic. */
function costNotes(view: string): string[] {
  if (view === 'list') return []
  return view === 'upstream' ? ['cost_weighted_hit_uses_list', 'upstream_partial_coverage'] : ['cost_weighted_hit_uses_list']
}

export interface MockOptions {
  now?: () => Date
  latencyMs?: number
  experiments?: 'running' | 'none'
}

function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function seedFrom(text: string): number {
  let h = 2166136261
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

function floorHour(date: Date): Date {
  return new Date(Math.floor(date.getTime() / HOUR_MS) * HOUR_MS)
}

function iso(ms: number): string {
  return new Date(ms).toISOString().replace('.000Z', 'Z')
}

function delay<T>(value: T, ms: number): Promise<T> {
  if (ms <= 0) return Promise.resolve(value)
  return new Promise((resolve) => setTimeout(() => resolve(value), ms))
}

/** Synthetic deploy timeline relative to `asOf`, shared by the trend and the trace counters. */
function mockEvents(asOfMs: number): PanelEvent[] {
  const release = (hoursAgo: number, label: string, kind: 'release' | 'restart' = 'release'): PanelEvent => {
    const at = asOfMs - hoursAgo * HOUR_MS - 13 * 60_000
    return { at: iso(at), kind, label, version: label.replace(/^v/, ''), cold_until: iso(at + HOUR_MS) }
  }
  return [
    release(150, 'v0.4.5'),
    release(76, 'v0.4.6'),
    release(18, 'v0.4.6', 'restart'),
    { at: iso(asOfMs - 18 * HOUR_MS - 13 * 60_000), kind: 'config', label: 'observability rev 7', policy_key: 'observability', cold_until: null },
  ]
}

function processStartedAt(asOfMs: number): string {
  return iso(asOfMs - 18 * HOUR_MS - 13 * 60_000)
}

interface HourSample {
  requests: number
  prompt: number
  upper: number
  lower: number
  requestHit: number
  opportunity: number
}

function hourSample(hourMs: number, events: PanelEvent[]): HourSample {
  const rand = mulberry32(seedFrom(`hour:${hourMs}`))
  const hourOfDay = new Date(hourMs).getUTCHours()
  const diurnal = 0.55 + 0.45 * Math.sin(((hourOfDay - 6) / 24) * Math.PI * 2)
  const requests = Math.round(380 + 820 * Math.max(diurnal, 0.12) + rand() * 90)
  const prompt = Math.round(requests * (128_000 + rand() * 22_000))
  let upper = 0.803 + 0.011 * Math.sin((hourMs / HOUR_MS / 24) * Math.PI * 2 + 1.3) + (rand() - 0.5) * 0.012
  for (const ev of events) {
    if (!ev.cold_until) continue
    const start = Date.parse(ev.at)
    const end = Date.parse(ev.cold_until)
    if (hourMs + HOUR_MS > start && hourMs < end) upper -= 0.047
  }
  // A synthetic step after the second release so the ruler has something to explain.
  if (hourMs > Date.parse(events[1].at)) upper -= 0.009
  upper = Math.min(Math.max(upper, 0.6), 0.95)
  const lower = upper - 0.0002 - rand() * 0.0005
  const requestHit = Math.min(0.985, upper + 0.098 + (rand() - 0.5) * 0.02)
  const opportunity = Math.round(prompt * (1 - upper) * (0.52 + rand() * 0.12))
  return { requests, prompt, upper, lower, requestHit, opportunity }
}

function aggregate(samples: HourSample[]) {
  const requests = samples.reduce((s, x) => s + x.requests, 0)
  const prompt = samples.reduce((s, x) => s + x.prompt, 0)
  const readUpper = samples.reduce((s, x) => s + x.prompt * x.upper, 0)
  const readLower = samples.reduce((s, x) => s + x.prompt * x.lower, 0)
  const hitRequests = samples.reduce((s, x) => s + x.requests * x.requestHit, 0)
  const opportunity = samples.reduce((s, x) => s + x.opportunity, 0)
  return {
    requests,
    prompt,
    read: Math.round(readUpper),
    upper: prompt ? readUpper / prompt : null,
    lower: prompt ? readLower / prompt : null,
    requestHit: requests ? hitRequests / requests : null,
    opportunity,
  }
}

export function mockNorthStar(query: NorthStarQuery, now: Date): NorthStarResponse {
  const asOf = floorHour(now).getTime()
  const hours = WINDOW_HOURS[query.window]
  const start = asOf - hours * HOUR_MS
  const granularity: Granularity = query.granularity ?? (query.window === '30d' ? '1d' : '1h')
  const costView = query.cost_view ?? 'list'
  const costMultiplier = COST_MULTIPLIER[costView] ?? 1
  const events = mockEvents(asOf)
  const samples: { hour: number; sample: HourSample }[] = []
  for (let h = start; h < asOf; h += HOUR_MS) samples.push({ hour: h, sample: hourSample(h, events) })

  const bucketMs = granularity === '1d' ? 24 * HOUR_MS : HOUR_MS
  const buckets = new Map<number, HourSample[]>()
  for (const { hour, sample } of samples) {
    const key = Math.floor(hour / bucketMs) * bucketMs
    const list = buckets.get(key) ?? []
    list.push(sample)
    buckets.set(key, list)
  }
  const series: NorthStarPoint[] = [...buckets.entries()]
    .sort(([a], [b]) => a - b)
    .map(([bucket, list]) => {
      const agg = aggregate(list)
      return {
        bucket_start: iso(bucket),
        requests: agg.requests,
        prompt_tokens: agg.prompt,
        token_hit_upper: agg.upper,
        token_hit_lower: agg.lower,
        request_hit: agg.requestHit,
        opportunity_recorded_tokens: agg.opportunity,
        partial: list.length * HOUR_MS < bucketMs,
        saved_amount: agg.read * SYNTHETIC_W * costMultiplier,
        opportunity_amount: agg.opportunity * SYNTHETIC_W * costMultiplier,
        token_hit_cost_weighted_upper: agg.upper === null ? null : Math.min(0.99, agg.upper + 0.017),
      }
    })

  const total = aggregate(samples.map((s) => s.sample))
  const previousSamples: HourSample[] = []
  for (let h = start - hours * HOUR_MS; h < start; h += HOUR_MS) previousSamples.push(hourSample(h, events))
  const previous = aggregate(previousSamples)
  const businessPrompt = Math.round(total.prompt / 0.9808)

  return {
    policy_version: MOCK_POLICY_VERSION,
    as_of: iso(asOf),
    generated_at: now.toISOString(),
    window: { start: iso(start), end: iso(asOf) },
    granularity,
    platform: query.platform,
    scope: {
      name: 'strict_known_minimum',
      business_prompt_tokens: businessPrompt,
      excluded: {
        media_requests: Math.round(total.requests * 0.009),
        zero_prompt_requests: Math.round(total.requests * 0.0007),
        probe_group_requests: Math.round(total.requests * 0.025),
        below_min_requests: Math.round(total.requests * 0.094),
        unknown_threshold_requests: Math.round(total.requests * 0.027),
      },
    },
    summary: {
      requests: total.requests,
      prompt_tokens: total.prompt,
      read_tokens: total.read,
      token_hit_upper: total.upper,
      token_hit_lower: total.lower,
      request_hit: total.requestHit,
      opportunity_recorded_tokens: total.opportunity,
      business_coverage: total.prompt / businessPrompt,
      standardized_hit: total.upper === null ? null : total.upper + 0.0007,
      standardized_weight_coverage: 0.996,
      cost: {
        currency: 'USD',
        view: costView,
        pricing_source: 'settled_cost',
        pricing_changed_in_window: false,
        priced_p_share: 0.9712,
        upstream_covered_p_share: costView === 'upstream' ? 0.884 : null,
        priced_models: 4,
        saved_amount: total.read * SYNTHETIC_W * costMultiplier,
        opportunity_amount: total.opportunity * SYNTHETIC_W * costMultiplier,
        token_hit_cost_weighted_upper: total.upper === null ? null : Math.min(0.99, total.upper + 0.017),
        token_hit_cost_weighted_lower: total.lower === null ? null : Math.min(0.99, total.lower + 0.017),
        previous: { saved_amount: previous.read * SYNTHETIC_W * costMultiplier, token_hit_cost_weighted_upper: previous.upper === null ? null : previous.upper + 0.017 },
        notes: costNotes(costView),
      },
      previous:
        previous.upper === null || previous.lower === null || previous.requestHit === null
          ? null
          : { token_hit_upper: previous.upper, token_hit_lower: previous.lower, request_hit: previous.requestHit },
    },
    series,
    events: events.filter((ev) => Date.parse(ev.at) >= start),
  }
}

interface Candidate {
  key: string
  label: string
  weight: number
  hit: number
  requestHit: number
  meta?: BreakdownRow['meta']
}

const HOSTS = ['relay-a.example', 'relay-b.example', 'relay-c.example', 'relay-d.example', 'relay-e.example',
  'relay-f.example', 'oauth:openai', 'relay-g.example', 'relay-h.example', 'relay-i.example', 'relay-j.example',
  'relay-k.example', 'relay-l.example']

function candidates(dimension: BreakdownDimension): Candidate[] {
  const rand = mulberry32(seedFrom(`dim:${dimension}`))
  const make = (key: string, label: string, weight: number, meta?: Candidate['meta']): Candidate => ({
    key,
    label,
    weight,
    hit: 0.74 + rand() * 0.17,
    requestHit: 0.8 + rand() * 0.18,
    meta,
  })
  switch (dimension) {
    case 'group':
      return [
        make('101', 'codex-pro', 51, { platform: 'openai' }),
        make('102', 'codex-team', 16, { platform: 'openai' }),
        make('103', 'general-mix', 15, { platform: 'openai' }),
        make('104', 'chat-compat', 5.2, { platform: 'openai' }),
        make('105', 'image-lite', 4.1, { platform: 'openai' }),
        make('106', 'claude-max', 3.4, { platform: 'anthropic' }),
        make('107', 'codex-fallback', 1.2, { platform: 'openai' }),
        make('108', 'grok-std', 0.9, { platform: 'grok' }),
        make('109', 'gemini-pro', 0.8, { platform: 'gemini' }),
        make('110', 'eval-batch', 0.7, { platform: 'openai' }),
        make('111', 'team-sandbox', 0.6, { platform: 'openai' }),
        make('112', 'legacy-chat', 0.5, { platform: 'openai' }),
      ]
    case 'account':
      return Array.from({ length: 16 }, (_, i) => {
        const host = HOSTS[i % 7]
        return make(String(301 + i), `acct-${host.replace(/[.:]/g, '-')}-${String(i + 1).padStart(2, '0')}`,
          28 / (i + 1.4), { platform: 'openai', upstream_host: host })
      })
    case 'upstream_host':
      return HOSTS.map((host, i) => make(host, host, [32.7, 24.4, 17.2, 5.8, 4.2, 3.6, 2.4, 2.7, 1.8, 1.2, 1.4, 0.9, 0.6][i], { accounts: 3 + ((i * 5) % 11) }))
    case 'client_family':
      return [
        make('codex-family', 'codex-family', 52.6), make('go-http-client', 'go-http-client', 13.3),
        make('litellm', 'litellm', 9.5), make('other', 'other', 7.8), make('codex_cli_rs', 'codex_cli_rs', 5.7),
        make('openai-python-sdk', 'openai-python-sdk', 3.4), make('claude-cli', 'claude-cli', 3.0),
        make('codex-vscode', 'codex-vscode', 3.0), make('opencode', 'opencode', 0.9),
        make('openai-sdk-unspecified', 'openai-sdk-unspecified', 0.5),
      ]
    case 'model':
      return [
        make('model-synthetic-a', 'model-synthetic-a', 48, { platform: 'openai', w_m: 1.12, priced: true }),
        make('model-synthetic-b', 'model-synthetic-b', 27, { platform: 'openai', w_m: 0.9, priced: true }),
        make('model-synthetic-c', 'model-synthetic-c', 12, { platform: 'anthropic', w_m: 2.7, priced: true }),
        make('model-synthetic-d', 'model-synthetic-d', 6, { platform: 'grok', w_m: null, priced: false }),
        make('model-synthetic-e', 'model-synthetic-e', 4, { platform: 'gemini', w_m: 0.6, priced: true }),
      ]
    case 'protocol':
      return [
        make('responses_http', 'responses_http', 74.7), make('chat_completions_http', 'chat_completions_http', 19.6),
        make('messages', 'messages', 4.8), make('gemini', 'gemini', 1.0),
      ]
  }
}

export function mockBreakdown(query: BreakdownQuery, now: Date): BreakdownResponse {
  const ns = mockNorthStar({ window: query.window, platform: query.platform }, now)
  const costView = query.cost_view ?? 'list'
  const costMultiplier = COST_MULTIPLIER[costView] ?? 1
  const totalPrompt = ns.scope.business_prompt_tokens
  const totalRequests = Math.round(ns.summary.requests / 0.973)
  const totalOpportunity = Math.round(ns.summary.opportunity_recorded_tokens / 0.99)
  const list = candidates(query.dimension)
  const weightSum = list.reduce((s, c) => s + c.weight, 0)
  const rows: BreakdownRow[] = list.map((c) => {
    const share = c.weight / weightSum
    const prompt = Math.round(totalPrompt * share)
    const opportunity = Math.round(totalOpportunity * share * (1.2 - (c.hit - 0.74)))
    return {
      key: c.key,
      label: c.label,
      meta: c.meta,
      requests: Math.round(totalRequests * share),
      prompt_tokens: prompt,
      read_tokens: Math.round(prompt * c.hit),
      token_hit_upper: c.hit,
      token_hit_lower: c.hit - 0.0003,
      request_hit: c.requestHit,
      opportunity_recorded_tokens: opportunity,
      opportunity_share: null,
      saved_amount: Math.round(prompt * c.hit) * SYNTHETIC_W * costMultiplier,
      opportunity_amount: opportunity * SYNTHETIC_W * costMultiplier * (c.meta?.priced === false ? 0 : 1),
      opportunity_amount_share: null,
      token_hit_cost_weighted_upper: Math.min(0.99, c.hit + 0.017),
    }
  })
  const opportunitySum = rows.reduce((s, r) => s + r.opportunity_recorded_tokens, 0)
  const amountSum = rows.reduce((s, r) => s + (r.opportunity_amount ?? 0), 0)
  for (const r of rows) {
    r.opportunity_share = opportunitySum ? r.opportunity_recorded_tokens / opportunitySum : null
    r.opportunity_amount_share = amountSum ? (r.opportunity_amount ?? 0) / amountSum : null
  }

  const sortKey = (r: BreakdownRow) =>
    query.sort === 'prompt'
      ? r.prompt_tokens
      : query.sort === 'token_hit'
        ? (r.token_hit_upper ?? 0)
        : query.sort === 'opportunity_amount'
          ? (r.opportunity_amount ?? 0)
          : r.opportunity_recorded_tokens
  rows.sort((a, b) => sortKey(b) - sortKey(a))
  const limit = query.limit ?? 10
  const top = rows.slice(0, limit)
  const rest = rows.slice(limit)
  const sum = (key: 'requests' | 'prompt_tokens' | 'read_tokens' | 'opportunity_recorded_tokens', from: BreakdownRow[]) =>
    from.reduce((s, r) => s + r[key], 0)
  const other = rest.length
    ? {
        count: rest.length,
        requests: sum('requests', rest),
        prompt_tokens: sum('prompt_tokens', rest),
        read_tokens: sum('read_tokens', rest),
        token_hit_upper: sum('read_tokens', rest) / sum('prompt_tokens', rest),
        token_hit_lower: sum('read_tokens', rest) / sum('prompt_tokens', rest) - 0.0003,
        request_hit: rest.reduce((s, r) => s + r.requests * (r.request_hit ?? 0), 0) / sum('requests', rest),
        opportunity_recorded_tokens: sum('opportunity_recorded_tokens', rest),
        opportunity_share: opportunitySum ? sum('opportunity_recorded_tokens', rest) / opportunitySum : null,
        saved_amount: rest.reduce((s, r) => s + (r.saved_amount ?? 0), 0),
        opportunity_amount: rest.reduce((s, r) => s + (r.opportunity_amount ?? 0), 0),
        opportunity_amount_share: amountSum ? rest.reduce((s, r) => s + (r.opportunity_amount ?? 0), 0) / amountSum : null,
        token_hit_cost_weighted_upper: Math.min(0.99, sum('read_tokens', rest) / sum('prompt_tokens', rest) + 0.017),
      }
    : null

  return {
    policy_version: MOCK_POLICY_VERSION,
    as_of: ns.as_of,
    generated_at: ns.generated_at,
    window: ns.window,
    dimension: query.dimension,
    sort: query.sort,
    platform: query.platform,
    cost: { currency: 'USD', view: costView, pricing_source: 'settled_cost', pricing_changed_in_window: false, notes: costNotes(costView) },
    total: {
      requests: sum('requests', rows),
      prompt_tokens: sum('prompt_tokens', rows),
      read_tokens: sum('read_tokens', rows),
      opportunity_recorded_tokens: opportunitySum,
    },
    rows: top,
    other,
  }
}

// [class, requests, prompt M, read M, opportunity M | null] for a full 24h window.
const LOSS_CLASSES_24H: [string, number, number, number, number | null][] = [
  ['full', 30210, 4120, 3986, 21],
  ['under_read', 6120, 1180, 702, 361],
  ['prefix_changed', 2310, 410, 214, 142],
  ['provider_reasoning_drop_suspect', 610, 121, 61, 44],
  ['other_conv', 1450, 232, 118, 68],
  ['cold', 3904, 392, 51, 0],
  ['below_min', 2108, 1.2, 0.2, 0],
  ['n/a_plugin', 3302, 470, 391, null],
  ['n/a_usage', 41, 4.1, 0, null],
  ['n/a_sampled_out', 822, 151, 99, null],
  ['n/a_truncated', 95, 31, 22, null],
]

export function mockTraceStats(query: TraceStatsQuery, now: Date): TraceStatsResponse {
  const nowMs = now.getTime()
  const asOf = floorHour(now).getTime()
  const windowMs = query.window === '1h' ? HOUR_MS : 24 * HOUR_MS
  const started = processStartedAt(asOf)
  const effectiveStart = Math.max(nowMs - windowMs, Date.parse(started))
  const scale = (effectiveStart < nowMs ? nowMs - effectiveStart : 0) / (24 * HOUR_MS)
  const enabled = query.platform === 'openai' || query.platform === 'all'
  const M = 1_000_000
  const loss_classes: LossClassStat[] = enabled
    ? LOSS_CLASSES_24H.map(([cls, req, p, read, opp]) => ({
        class: cls,
        requests: Math.round(req * scale),
        prompt_tokens: Math.round(p * M * scale),
        read_tokens: Math.round(read * M * scale),
        opportunity_tokens: opp === null ? null : Math.round(opp * M * scale),
        excluded: cls.startsWith('n/a_'),
      }))
    : []
  const tracedRequests = loss_classes.reduce((s, c) => s + c.requests, 0)
  const tracedPrompt = loss_classes.reduce((s, c) => s + c.prompt_tokens, 0)
  const businessPrompt = Math.round(tracedPrompt / 0.9649)
  const scaled = (n: number) => Math.round(n * scale)

  return {
    as_of: now.toISOString(),
    generated_at: now.toISOString(),
    window: { start: iso(nowMs - windowMs), end: now.toISOString() },
    effective_start: iso(effectiveStart),
    process_started_at: started,
    trace_enabled: enabled,
    trace_policy_rev: 7,
    schema_version: 1,
    platforms_covered: ['openai'],
    scope_notes: ['messages_bridge_excluded'],
    coverage: {
      business_requests: Math.round(tracedRequests / 0.958),
      business_prompt_tokens: businessPrompt,
      traced_requests: tracedRequests,
      traced_prompt_tokens: tracedPrompt,
      coverage_ratio: enabled && tracedPrompt ? tracedPrompt / businessPrompt : null,
      category_known_ratio: enabled ? 0.9962 : null,
      targets: { coverage_ratio: 0.95, category_known_ratio: 0.99 },
    },
    loss_classes,
    pck_in: enabled
      ? [
          { class: 'none', requests: scaled(18110), prompt_tokens: scaled(2210 * M), read_tokens: scaled(1742 * M) },
          { class: 'client_uuid', requests: scaled(21980), prompt_tokens: scaled(3420 * M), read_tokens: scaled(2851 * M) },
          { class: 'client_non_uuid', requests: scaled(9620), prompt_tokens: scaled(1340 * M), read_tokens: scaled(1022 * M) },
          { class: 'empty', requests: scaled(1260), prompt_tokens: scaled(140 * M), read_tokens: scaled(104 * M) },
        ]
      : [],
    pck_out: enabled
      ? [
          { class: 'passthrough', relay_family: 'unknown', requests: scaled(30880), prompt_tokens: scaled(4610 * M), read_tokens: scaled(3790 * M), under_read_tokens: scaled(610 * M) },
          { class: 'oauth_rewritten', relay_family: 'unknown', requests: scaled(7010), prompt_tokens: scaled(890 * M), read_tokens: scaled(802 * M), under_read_tokens: scaled(52 * M) },
          { class: 'gw_compat_hex', relay_family: 'unknown', requests: scaled(6400), prompt_tokens: scaled(720 * M), read_tokens: scaled(561 * M), under_read_tokens: scaled(171 * M) },
          { class: 'none', relay_family: 'unknown', requests: scaled(5120), prompt_tokens: scaled(690 * M), read_tokens: scaled(527 * M), under_read_tokens: scaled(214 * M) },
          { class: 'gw_from_header', relay_family: 'unknown', requests: scaled(1180), prompt_tokens: scaled(160 * M), read_tokens: scaled(131 * M), under_read_tokens: scaled(31 * M) },
          { class: 'gw_grok', relay_family: 'unknown', requests: scaled(410), prompt_tokens: scaled(40 * M), read_tokens: scaled(36 * M), under_read_tokens: scaled(2 * M) },
        ]
      : [],
    pck_out_is_uuid_ratio: enabled ? 0.312 : null,
    session_headers: enabled
      ? [
          { header: 'session_id', in_ratio: 0.642, out_ratio: 0.713 },
          { header: 'x-codex-turn-state', in_ratio: 0.481, out_ratio: 0.481 },
          { header: 'thread_id', in_ratio: 0.214, out_ratio: 0.214 },
          { header: 'conversation_id', in_ratio: 0.122, out_ratio: 0.035 },
          { header: 'x-client-request-id', in_ratio: 0.093, out_ratio: 0.093 },
          { header: 'session-id', in_ratio: 0.041, out_ratio: 0.122 },
        ]
      : [],
    sess_out_relation: enabled
      ? [
          { relation: 'client_value', requests: scaled(30120), prompt_tokens: scaled(4100 * M) },
          { relation: 'gw_isolated', requests: scaled(7010), prompt_tokens: scaled(890 * M) },
          { relation: 'uuid_of_pck', requests: scaled(6400), prompt_tokens: scaled(720 * M) },
          { relation: 'fingerprint_converged', requests: scaled(1620), prompt_tokens: scaled(210 * M) },
          { relation: 'none', requests: scaled(5850), prompt_tokens: scaled(790 * M) },
        ]
      : [],
    route_reasons: enabled
      ? [
          { reason: 'sticky_hit', requests: scaled(39210), prompt_tokens: scaled(5610 * M), read_tokens: scaled(4730 * M) },
          { reason: 'sticky_new', requests: scaled(6020), prompt_tokens: scaled(640 * M), read_tokens: scaled(402 * M) },
          { reason: 'overflow', requests: scaled(1810), prompt_tokens: scaled(270 * M), read_tokens: scaled(143 * M) },
          { reason: 'failover_n', requests: scaled(1220), prompt_tokens: scaled(190 * M), read_tokens: scaled(77 * M) },
          { reason: 'session_rebind', requests: scaled(380), prompt_tokens: scaled(66 * M), read_tokens: scaled(21 * M) },
          { reason: 'soft_divert', requests: scaled(610), prompt_tokens: scaled(92 * M), read_tokens: scaled(58 * M) },
          { reason: 'last_resort', requests: scaled(40), prompt_tokens: scaled(6 * M), read_tokens: scaled(2 * M) },
        ]
      : [],
    rebind_causes: enabled
      ? [
          { cause: 'stall', requests: scaled(161) },
          { cause: 'pre_output_error', requests: scaled(122) },
          { cause: 'first_event_timeout', requests: scaled(71) },
          { cause: 'post_output', requests: scaled(26) },
        ]
      : [],
    model_isolated_switch_rate: enabled ? 0.0412 : null,
    host_read_ewma: enabled
      ? HOSTS.slice(0, 5).map((host, i) => ({ host, read_share: 0.77 + i * 0.022, samples: 3120 - i * 470, half_life_s: 1800 }))
      : [],
  }
}

export function mockExperiments(now: Date, mode: 'running' | 'none'): ExperimentsResponse {
  const asOf = new Date(Math.floor(now.getTime() / 600_000) * 600_000)
  if (mode === 'none') return { as_of: asOf.toISOString(), generated_at: now.toISOString(), experiments: [] }
  const experiment: Experiment = {
    id: 'EXP-1',
    lever: 'L-01',
    title: 'UUID session key fill',
    status: 'running',
    started_at: iso(floorHour(now).getTime() - 40 * HOUR_MS),
    ended_at: null,
    build_id: 'v0.4.6+p0a+l01',
    phase: 'responses_fill',
    policy_rev: 3,
    unit: 'session_bucket',
    planned_per_arm: 400,
    arms: [
      { arm: 'off', sessions: 312, prompt_tokens: 905_120_000, read_tokens: 712_100_000, token_hit: 0.7868, under_read_share: 0.212, switches_per_100: 3.1, errors: 41, attempts: 10422, undelivered: 6 },
      { arm: 'on', sessions: 305, prompt_tokens: 889_400_000, read_tokens: 727_300_000, token_hit: 0.8177, under_read_share: 0.171, switches_per_100: 3.0, errors: 39, attempts: 10118, undelivered: null },
    ],
    slices: [
      { slice: 's_r', sessions_off: 312, sessions_on: 305, diff_pp: 3.09, ci_low_pp: -0.42, ci_high_pp: 6.83, ci_ready: true },
      { slice: 's_all', sessions_off: 1180, sessions_on: 1162, diff_pp: 1.02, ci_low_pp: -1.31, ci_high_pp: 3.4, ci_ready: true },
      { slice: 's_c_first_2h', sessions_off: 24, sessions_on: 21, diff_pp: null, ci_low_pp: null, ci_high_pp: null, ci_ready: false },
    ],
    criteria: [
      { id: 'stop_s_all_drop', slice: 's_all', met: false },
      { id: 's_r_positive', slice: 's_r', met: false },
      { id: 's_r_safe', slice: 's_r', met: null },
      { id: 's_all_noninferior', slice: 's_all', met: null },
    ],
    verdict: 'insufficient_sample',
  }
  return { as_of: asOf.toISOString(), generated_at: now.toISOString(), experiments: [experiment] }
}

export function createMockCachePanelClient(options: MockOptions = {}): CachePanelClient {
  const now = options.now ?? (() => new Date())
  const latency = options.latencyMs ?? 220
  const experiments = options.experiments ?? 'running'
  return {
    getNorthStar: (query) => delay(mockNorthStar(query, now()), latency),
    getBreakdown: (query) => delay(mockBreakdown(query, now()), latency),
    getTraceStats: (query) => delay(mockTraceStats(query, now()), latency),
    listExperiments: () => delay(mockExperiments(now(), experiments), latency),
  }
}
