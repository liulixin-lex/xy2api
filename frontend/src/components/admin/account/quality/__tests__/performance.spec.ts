import { describe, expect, it } from 'vitest'

import { lastEval, perfGroup, perfRow } from '@/__tests__/fixtures/schedQuality/performance'
import {
  DEFAULT_SORT,
  evidenceElsewhere,
  fmtPoints,
  fmtScore,
  fmtSigned,
  nextSort,
  noAction,
  countText,
  fmtAmountSigned,
  fmtShortfall,
  fmtTenths,
  modelLabel,
  reasonLong,
  noActionShort,
  perfView,
  perfViews,
  sortViews,
  wilson
} from '../performance'

const t = (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
const te = (key: string) => !key.endsWith('.brand_new')
const group = perfGroup()
const view = (...args: Parameters<typeof perfRow>) => perfView(perfRow(...args), group)
const order = (views: ReturnType<typeof perfViews>) => views.map((item) => item.row.account_id)

describe('performance figures', () => {
  // The design's examples (shape of the round-3 review's table): slow and busy, slow but within 10 points, clearly fast.
  it('marks slow only past 10 points with the cautious bound above expected, and fast the other way', () => {
    const slow = view(483, {}, { n: 463, slowRate: 0.531, expectedRate: 0.197 })
    expect([slow.slowMark, slow.red, fmtPoints(slow.excess!), slow.extra]).toEqual(['slow', true, '+33.4', 155])
    const busy = view(615, {}, { n: 1066, slowRate: 0.189, expectedRate: 0.116 })
    expect([busy.slowMark, busy.red, fmtPoints(busy.excess!)]).toEqual([null, false, '+7.3'])
    const fast = view(285, {}, { n: 5139, slowRate: 0.097, expectedRate: 0.225 })
    expect([fast.slowMark, fast.red, fmtPoints(fast.excess!)]).toEqual(['fast', false, '−12.8'])
  })

  // Review item 1: worked out on the counts, so a gap of exactly 10 points is 10 points, and half a request rounds up.
  it('marks exactly 10 points over or under, from the counts, not from a float gap', () => {
    const exact = (slow: number, expected: number, n = 100) => view(1, { first: { slow, expected_slow: expected, expected_n: n, n } }, { n })
    const slow = exact(30, 20)
    expect([slow.excessTenths, fmtPoints(slow.excess!), slow.extra, slow.slowMark]).toEqual([100, '+10.0', 10, 'slow'])
    const fast = exact(20, 30)
    expect([fast.excessTenths, fmtPoints(fast.excess!), fast.extra, fast.slowMark]).toEqual([-100, '−10.0', -10, 'fast'])
    expect(exact(3, 2.5, 20).extra).toBe(1)
    expect(exact(3, 2.4999999999999996, 20).extra).toBe(1)
    // 12 points over, but 8 of 20 has a lower bound of 27.26%, under the 28% expected: not marked.
    const small = exact(8, 5.6, 20)
    expect(wilson(8, 20).lower).toBeCloseTo(0.2726, 4)
    expect([fmtPoints(small.excess!), small.slowMark]).toEqual(['+12.0', null])
  })

  // Re-review item 1: the 10 points are judged on the counts; only the display rounds 9.99 to 10.0.
  it.each([
    [2999, 2000, '+10.0', null],
    [3000, 2000, '+10.0', 'slow'],
    [2000, 2999, '−10.0', null],
    [2000, 3000, '−10.0', 'fast']
  ])('marks %i slow against %i expected of 10,000 (%s) as %s', (slow, expected, shown, mark) => {
    const item = view(1, { first: { slow, expected_slow: expected, expected_n: 10_000, n: 10_000 } }, { n: 10_000 })
    expect([fmtPoints(item.excess!), item.slowMark]).toEqual([shown, mark])
  })

  // Increment review item 1: a fractional expected count (a sum of per-request shares) exactly 10 points away still
  // marks; the float products must not tip it.
  it.each([
    ['slow', 2268, 2106, (2268 * 29) / 35, '+10.0'],
    ['fast', 2088, 1827, (2088 * 39) / 40, '−10.0']
  ])('marks a fractional expected count exactly 10 points away as %s', (mark, n, slow, expected, shown) => {
    const item = view(1, { first: { n, slow, expected_n: n, expected_slow: expected } }, { n })
    expect([fmtTenths(item.excessTenths!), item.slowMark]).toEqual([shown, mark])
  })

  // Increment review item 2: the display is the rounded tenths, not a second rounding of the float (7.25 is +7.3).
  it('shows the excess from its rounded tenths', () => {
    const item = view(1, { first: { n: 400, slow: 109, expected_n: 400, expected_slow: 80 } }, { n: 400 })
    expect([item.excessTenths, fmtTenths(item.excessTenths!), fmtTenths(-128), fmtTenths(0)]).toEqual([73, '+7.3', '−12.8', '0.0'])
  })

  it('does not mark 6 slow of 20 against 20% expected: 10 points over, but the lower bound is under expected', () => {
    expect(wilson(6, 20).lower).toBeCloseTo(0.188, 3)
    const edge = view(1, {}, { n: 20, slowRate: 0.3, expectedRate: 0.2 })
    expect([fmtPoints(edge.excess!), edge.slowMark]).toEqual(['+10.0', null])
  })

  it('gives no share, excess or extra below the minimums: 20 first sends from 2 sessions, 20 comparable ones', () => {
    expect(view(1, {}, { n: 19, slowRate: 0.9 }).slowRate).toBeNull()
    const oneSession = view(1, { first: { sessions: 1 } }, { n: 200, slowRate: 0.9 })
    expect([oneSession.slowRate, oneSession.excess, oneSession.slowMark]).toEqual([null, null, null])
    // Enough first sends, too few comparable ones: the expected share is shown, not set against.
    const few = view(1, { first: { expected_n: 12, expected_slow: 3 } }, { n: 100, slowRate: 0.6 })
    expect([few.slowRate, few.expectedRate, few.expectedEnough, few.excess, few.red]).toEqual([0.6, 0.25, false, null, false])
    const none = view(1, {}, { n: 100, slowRate: 0.6, expectedRate: null })
    expect([none.expectedRate, none.excess, none.extra]).toEqual([null, null, null])
    expect(view(1, { cont: { turns: 19 } }).cacheRate).toBeNull()
    expect(view(1, { cont: { sessions: 1 } }).cacheRate).toBeNull()
    expect(view(1, { attempts: { n: 19, timeouts: 3 } }).timeoutRate).toBeNull()
    const short = view(1, { first: { sessions: 1 }, cont: { turns: 3 }, attempts: { n: 4 } }, { n: 7, slowRate: 0.43 })
    expect(short.allShort).toBe(true)
  })

  it('marks the cache red under the group\'s own minimum, with enough continued turns', () => {
    expect(view(1, { cont: { read_tokens: 790 } }).cacheLow).toBe(true)
    expect(view(1, { cont: { read_tokens: 800 } }).cacheLow).toBe(false)
    expect(perfView(perfRow(1, { cont: { read_tokens: 790 } }), perfGroup({ min_cache_ratio: 0.7 })).cacheLow).toBe(false)
  })

  it('works the share out of the group\'s requests, or of the model\'s when one is picked', () => {
    const rows = [perfRow(1, { requests: 300 }), perfRow(2, { requests: 100 }), perfRow(3, { requests: 600, model: 'gpt-y' })]
    expect(perfViews(rows, [group], { byModel: false }).map((item) => item.share)).toEqual([0.3, 0.1, 0.6])
    expect(perfViews(rows, [group], { byModel: true }).map((item) => item.share)).toEqual([0.75, 0.25, 1])
    expect(perfViews(rows, [group], { byModel: true, whole: false }).map((item) => item.share)).toEqual([null, null, null])
  })

  // Review item 11: another group's member of the same model and priority is not a peer.
  it('ranks within one group: the same model and priority in another group ranks on its own', () => {
    const scored = (id: number, groupId: number, value: number) => perfRow(id, { group_id: groupId, score: { value, n: 40, sessions: 4, excess: 3 } })
    const views = perfViews([scored(1, 18, 0.2), scored(2, 18, 0.1), scored(3, 50, 0.05)], [group, perfGroup({ group_id: 50 })], { byModel: false })
    expect(views.map((item) => [item.row.account_id, item.rank])).toEqual([[1, { place: 2, of: 2 }], [2, { place: 1, of: 2 }], [3, { place: 1, of: 1 }]])
  })

  it('ranks by score only among scored members of the same group, model and priority; equal scores share a place', () => {
    const scored = (id: number, value: number | null, patch: Parameters<typeof perfRow>[1] = {}) => perfRow(id, { ...patch, score: { value, n: 40, sessions: 4, excess: 3 } })
    const rows = [
      scored(1, 0.25), scored(2, 0.09), scored(3, 0.25), scored(4, null),
      scored(5, 0.01, { member: false }),
      scored(6, 0.3, { priority: 1 }),
      scored(7, 0.5, { model: 'gpt-y' })
    ]
    const ranks = Object.fromEntries(perfViews(rows, [group], { byModel: false }).map((item) => [item.row.account_id, item.rank]))
    expect(ranks).toEqual({
      1: { place: 2, of: 3 }, 2: { place: 1, of: 3 }, 3: { place: 2, of: 3 }, 4: null, 5: null, 6: { place: 1, of: 1 }, 7: { place: 1, of: 1 }
    })
    expect(perfViews(rows, [group], { byModel: false, whole: false }).every((item) => item.rank === null)).toBe(true)
  })
})

describe('performance order', () => {
  const rows = [
    perfRow(1, {}, { n: 463, slowRate: 0.531 }), // extra +155
    perfRow(2, {}, { n: 1066, slowRate: 0.27 }), // extra +78
    perfRow(3, {}, { n: 5139, slowRate: 0.0686 }), // extra −659
    perfRow(4, { requests: 9 }, { n: 7, slowRate: 0.43 }), // short
    perfRow(5, { requests: 30, first: { sessions: 1 } }, { n: 30, slowRate: 0.5 }) // short, more requests
  ]
  const views = perfViews(rows, [group], { byModel: false })

  it('puts the most extra slow requests first by default, rows short of the minimum last', () => {
    expect(DEFAULT_SORT).toEqual({ key: 'extra', dir: 'desc' })
    expect(order(sortViews(views, DEFAULT_SORT))).toEqual([1, 2, 3, 5, 4])
  })

  it('keeps the short rows last in either direction, by requests among themselves', () => {
    expect(order(sortViews(views, { key: 'extra', dir: 'asc' }))).toEqual([3, 2, 1, 5, 4])
    expect(order(sortViews(views, { key: 'slow', dir: 'asc' }))).toEqual([3, 2, 1, 5, 4])
  })

  // Review item 2: a row short of everything never comes first, whatever the column (requests and switches have no
  // minimum of their own).
  it('puts rows short of every minimum last by requests and switches too, in either direction', () => {
    const full = perfView(perfRow(1, { requests: 100 }, { n: 100 }), group)
    const short = perfView(perfRow(2, { requests: 7, first: { sessions: 1 }, cont: { turns: 2 }, attempts: { n: 7 }, switches: { handoff: 2 } }, { n: 7 }), group)
    expect(short.allShort).toBe(true)
    for (const sort of [{ key: 'requests', dir: 'asc' }, { key: 'switches', dir: 'desc' }, { key: 'requests', dir: 'desc' }] as const) {
      expect(order(sortViews([short, full], sort))).toEqual([1, 2])
    }
  })

  it('shows each column\'s bad end first: the lowest cache, the highest of the rest; a second click turns it', () => {
    expect(nextSort(DEFAULT_SORT, 'cache')).toEqual({ key: 'cache', dir: 'asc' })
    expect(nextSort(DEFAULT_SORT, 'score')).toEqual({ key: 'score', dir: 'desc' })
    expect(nextSort({ key: 'cache', dir: 'asc' }, 'cache')).toEqual({ key: 'cache', dir: 'desc' })
  })
})

describe('why not acted', () => {
  const unknown = (latest = lastEval()) => ({ identity: 'unknown' as const, until: null, last_eval: latest })
  const quiet = { identity: 'pass' as const, until: null, last_eval: null }
  const slowRow = (lines: Parameters<typeof perfRow>[1]['lines'], patch: Parameters<typeof perfRow>[1] = {}) =>
    perfView(perfRow(1, { ...patch, lines }, { n: 463, slowRate: 0.531 }), group)

  it('gives the first-output line\'s reason on a slow row whose line is not at the rear and took no action', () => {
    const why = noAction(slowRow({ ttfo: unknown(), error: quiet, cache: quiet }))
    expect(why?.dim).toBe('ttfo')
    expect(noActionShort(why!.eval, t, te)).toBe('admin.schedQuality.noAction.short.peer_short')
  })

  it('says nothing when the line acted, was never assessed, the row is not red, not a member, or the gate is off', () => {
    expect(noAction(slowRow({ ttfo: unknown(lastEval({ acted: true, action: 'release', reason: 'within' })), error: quiet, cache: quiet }))).toBeNull()
    expect(noAction(slowRow({ ttfo: { identity: 'unknown', until: null, last_eval: null }, error: quiet, cache: quiet }))).toBeNull()
    expect(noAction(perfView(perfRow(1, { lines: { ttfo: unknown(), error: quiet, cache: quiet } }, { slowRate: 0.2 }), group))).toBeNull()
    expect(noAction(slowRow({ ttfo: unknown(), error: quiet, cache: quiet }, { member: false }))).toBeNull()
    expect(noAction(perfView(perfRow(1, { lines: { ttfo: unknown(), error: quiet, cache: quiet } }, { n: 463, slowRate: 0.531 }), perfGroup({ gate_enabled: false })))).toBeNull()
  })

  it('goes on to the cache line when the first-output line is already at the rear and the cache is low too', () => {
    const rear = { identity: 'rear' as const, until: '2026-11-19T12:40:00Z', last_eval: lastEval({ acted: true, action: 'enter', reason: 'over' }) }
    const lines = { ttfo: rear, error: quiet, cache: unknown(lastEval({ reason: 'own_short' })) }
    const both = slowRow(lines, { cont: { read_tokens: 500 } })
    expect(noAction(both)?.dim).toBe('cache')
    expect(noAction(slowRow(lines))).toBeNull()
  })

  it('writes the long reason with what held it up and how long, and other for a code it does not know', () => {
    expect(reasonLong(lastEval(), t, te)).toBe(
      'admin.schedQuality.noAction.long.peer_short:{"counts":"admin.schedQuality.noAction.counts:{\\"have\\":\\"8\\",\\"need\\":\\"20\\"}"}' +
      'admin.schedQuality.noAction.since:{"duration":"admin.schedQuality.duration.hoursMinutes:{\\"h\\":2,\\"m\\":50}"}'
    )
    // Weighted evidence reads with one decimal; nothing sent, nothing written.
    expect(reasonLong(lastEval({ have: 9.25, since: '2026-11-19T12:05:00Z' }), t, te)).toBe(
      'admin.schedQuality.noAction.long.peer_short:{"counts":"admin.schedQuality.noAction.counts:{\\"have\\":\\"9.3\\",\\"need\\":\\"20\\"}"}'
    )
    expect(reasonLong(lastEval({ have: null, need: null, since: '2026-11-19T11:30:00Z' }), t, te)).toBe(
      'admin.schedQuality.noAction.long.peer_short:{"counts":""}admin.schedQuality.noAction.since:{"duration":"admin.schedQuality.duration.minutes:{\\"m\\":35}"}'
    )
    expect(reasonLong(lastEval({ reason: 'brand_new', since: '2026-11-19T12:05:00Z' }), t, te)).toBe('admin.schedQuality.noAction.long.other')
    expect(noActionShort(lastEval({ reason: 'brand_new' }), t, te)).toBe('admin.schedQuality.noAction.short.other')
  })

  // Review item 4: a weighted shortfall that rounds to the need still reads as short.
  it('writes a shortfall that rounds to the need as under it', () => {
    expect(fmtShortfall(19.96, 20)).toEqual({ have: '<20', need: '20' })
    expect(fmtShortfall(9.25, 20)).toEqual({ have: '9.3', need: '20' })
    expect(fmtShortfall(20, 20)).toEqual({ have: '20', need: '20' })
    expect(reasonLong(lastEval({ have: 19.96, since: '2026-11-19T12:05:00Z' }), t, te)).toBe(
      'admin.schedQuality.noAction.long.peer_short:{"counts":"admin.schedQuality.noAction.counts:{\\"have\\":\\"<20\\",\\"need\\":\\"20\\"}"}'
    )
  })

  // Review item 8: the empty model of older switch records has a name of its own.
  it('names the empty model as unknown', () => {
    expect([modelLabel('', t), modelLabel('gpt-x', t)]).toEqual(['admin.schedQuality.perf.unknownModel', 'gpt-x'])
  })

  // 裁决 6: a line held at the rear on other groups' evidence alone says so.
  it('tells a rear line whose evidence all came from other groups', () => {
    expect(evidenceElsewhere('rear', { samples_local: 0, samples_total: 14 })).toBe(true)
    expect(evidenceElsewhere('rear', { samples_local: 2, samples_total: 14 })).toBe(false)
    expect(evidenceElsewhere('rear', { samples_local: 0, samples_total: 0 })).toBe(false)
    expect(evidenceElsewhere('rear', { samples_local: null, samples_total: 14 })).toBe(false)
    expect(evidenceElsewhere('unknown', { samples_local: 0, samples_total: 14 })).toBe(false)
  })
})

describe('performance formats', () => {
  it('shows the score in points like the excess, signed figures with a real minus', () => {
    expect([fmtScore(0.251), fmtScore(-0.059), fmtScore(0.03), fmtScore(-0.0001)]).toEqual(['25.1', '−5.9', '3.0', '0.0'])
    expect([fmtPoints(0.335), fmtPoints(-0.128), fmtPoints(0.0001)]).toEqual(['+33.5', '−12.8', '0.0'])
    expect([fmtSigned(155), fmtSigned(-658), fmtSigned(0), fmtSigned(1234)]).toEqual(['+155', '−658', '0', '+1,234'])
  })
})

describe('acceptance round 2 formats', () => {
  it('writes a negative amount with a real minus, as every other negative figure', () => {
    expect([fmtAmountSigned(-26.7), fmtAmountSigned(26.74), fmtAmountSigned(0)]).toEqual(['−26.7', '26.7', '0'])
  })

  it('asks the copy for a count with its number, so English can pick one turn or many', () => {
    const calls: unknown[][] = []
    const plural = (...args: unknown[]) => { calls.push(args); return 'x' }
    countText(plural as never, 'turns', 1)
    countText(plural as never, 'sessions', 1234)
    expect(calls).toEqual([
      ['admin.schedQuality.perf.count.turns', { n: '1' }, 1],
      ['admin.schedQuality.perf.count.sessions', { n: '1,234' }, 1234]
    ])
  })
})
