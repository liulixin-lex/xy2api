import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import { formatShortTime } from '@/components/admin/account/table/accountTime'

import type { BackendDecision, BackendDetailGroup, BackendGroupModel, BackendLastEval } from '@/api/admin/schedQualityTypes'
import { basis, cacheEvidence, errorEvidence, groupModel, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import {
  basisFigures, basisPeriod, basisReading, evidenceCauses, groupPlace, LACK_UNITS, lackUnit, lacksText, mergeDecisions, modelMarks, panelRecoverable, pickModelTab, placeLine, rearStay, trialNote
} from '../qualityPanel'
import { groupDotState } from '@/components/admin/account/table/accountGroups'

const group = (id: number, rows: BackendGroupModel[], exempt = false): BackendDetailGroup => ({
  group_id: id,
  name: `g${id}`,
  policy: { enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0, takeover_idle_seconds: 0 },
  exemption: exempt ? { group_id: 0, expires_at: null, reason: '' } : null,
  by_model: rows
})
const normal = { stop: null }

describe('quality panel place', () => {
  it('reads a group\'s place for a model in the order stopped, exempt, then the backend\'s placement', () => {
    const g = group(1, [groupModel('rear', { error: 'rear', ttfo: 'trial' }), groupModel('trial', { cache: 'trial' }), groupModel('front', { ttfo: 'unknown' })])
    expect(groupPlace({ stop: { source: 'auto', reason: 'rate_limit', until: null } }, g, 'rear')).toBe('stopped')
    expect(groupPlace(normal, group(1, [groupModel('rear', { error: 'rear' })], true), 'rear')).toBe('exempt')
    expect(groupPlace(normal, group(1, [groupModel('rear', { error: 'rear' }, { exempt: true })]), 'rear')).toBe('exempt')
    // Any line at the rear puts the account at the rear, even with another on trial; any trial → trial.
    expect(groupPlace(normal, g, 'rear')).toBe('rear')
    expect(groupPlace(normal, g, 'trial')).toBe('trial')
    expect(groupPlace(normal, g, 'front')).toBe('front')
    // No row for the model: no data, every line unknown, at the front.
    expect(groupPlace(normal, g, 'other')).toBe('front')
    // A model's tab is marked by its worst place: rear before trial; an exempt group is not at the rear.
    const detail = { ...normal, groups: [g, group(2, [groupModel('front', { cache: 'rear' })], true)] }
    expect([...modelMarks(detail)]).toEqual([['rear', 'rear'], ['trial', 'trial']])
  })

  // 7f-2: the detail has no `effective`; the panel reads the same three conditions the backend's flag stands for, so
  // the panel and the group's dot (which reads the summary's flag) say the same.
  it('is at the rear only while quality scheduling is on, fresh and the group\'s policy enabled, as the group\'s dot says', () => {
    const rear = group(1, [groupModel('a', { ttfo: 'rear' })])
    const off = { ...rear, policy: { ...rear.policy, enabled: false } }
    const summary = (effective: boolean) => ({
      group_status: { 1: effective ? ('rear' as const) : ('pass' as const) }, stop: null, exemptions: [], trial: [],
      rear: [{ group_id: 1, model: 'a', lines: ['ttfo' as const], until: '', exempt: false, effective }]
    })
    const cases = [
      { detail: { stop: null, global_enabled: true, stale: false, groups: [rear] }, effective: true },
      { detail: { stop: null, global_enabled: false, stale: false, groups: [rear] }, effective: false },
      { detail: { stop: null, global_enabled: true, stale: true, groups: [rear] }, effective: false },
      { detail: { stop: null, global_enabled: true, stale: false, groups: [off] }, effective: false }
    ]
    for (const { detail, effective } of cases) {
      const place = groupPlace(detail, detail.groups[0], 'a')
      expect(place).toBe(effective ? 'rear' : 'front')
      expect(groupDotState(summary(effective), 1)).toBe(effective ? 'back' : 'ok')
      expect(modelMarks(detail).get('a')).toBe(effective ? 'rear' : undefined)
    }
  })

  it('shows a rear place by its rear line that ends last and a trial by its trial that ends last; the other places by none', () => {
    const row = groupModel('a', { ttfo: 'rear', error: 'rear', cache: 'trial' })
    Object.assign(row.lines.ttfo, { level: 3, until: '2026-11-16T10:20:00Z' })
    Object.assign(row.lines.error, { level: 1, until: '2026-11-16T10:50:00Z' })
    Object.assign(row.lines.cache, { trial_started_at: '2026-11-16T09:00:00Z', trial_until: '2026-11-16T11:00:00Z' })
    expect(placeLine(row, 'rear')).toEqual({ dim: 'error', line: row.lines.error, held: 2 })
    expect(placeLine(row, 'trial')).toEqual({ dim: 'cache', line: row.lines.cache, held: 1 })
    expect(placeLine(row, 'front')).toBeNull()
    expect(placeLine(undefined, 'rear')).toBeNull()
  })

  // Astra review of fe, 3 and suggestion 2: an end that is missing or unreadable comes before every known one.
  it('takes a known end over one that is missing or unreadable, before or after it', () => {
    for (const unknownEnd of ['', 'not a time', null]) {
      for (const [first, second] of [['ttfo', 'error'], ['error', 'ttfo']] as const) {
        const row = groupModel('a', { ttfo: 'rear', error: 'rear' })
        Object.assign(row.lines[first], { until: unknownEnd })
        Object.assign(row.lines[second], { until: '2026-11-16T10:50:00Z' })
        expect(placeLine(row, 'rear')?.dim).toBe(second)
      }
    }
  })

  // Plan item 18: the rules have no steps; a rear stay is only when it is reviewed, and a missing end is a dash.
  it.each(['zh', 'en'] as const)('%s: shows a rear stay as its review time only, a dash when the end is missing', (locale) => {
    const { t } = createI18n({ legacy: false, locale, messages: { zh, en } }).global
    const until = '2026-11-19T10:40:00Z'
    const review = (time: string) => (locale === 'zh' ? `${time}\u00a0复核` : `review\u00a0${time}`)
    expect(rearStay(t, until)).toBe(review(formatShortTime(until)))
    for (const missing of [null, undefined, '']) expect(rearStay(t, missing)).toBe(review('—'))
  })

  it('takes the trial that ends last, whatever its start: three lines on trial from three starts', () => {
    const row = groupModel('a', { ttfo: 'trial', error: 'trial', cache: 'trial' })
    // An old 60-minute trial that started last but ends first, a new 120-minute one, and one without an end.
    Object.assign(row.lines.ttfo, { trial_started_at: '2026-11-16T10:30:00Z', trial_until: '2026-11-16T11:30:00Z' })
    Object.assign(row.lines.error, { trial_started_at: '2026-11-16T10:00:00Z', trial_until: '2026-11-16T12:00:00Z' })
    Object.assign(row.lines.cache, { trial_started_at: '2026-11-16T10:10:00Z', trial_until: null })
    expect(placeLine(row, 'trial')).toEqual({ dim: 'error', line: row.lines.error, held: 3 })
  })

  it('offers 恢复 for a line at the rear or on trial, not for unknown or pass', () => {
    const detail = (row: BackendGroupModel) => ({ groups: [group(1, [row])], auto_states: [] })
    expect(panelRecoverable(detail(groupModel('a', { cache: 'rear' })))).toBe(true)
    expect(panelRecoverable(detail(groupModel('a', { ttfo: 'trial' })))).toBe(true)
    expect(panelRecoverable(detail(groupModel('a', { ttfo: 'unknown' })))).toBe(false)
  })

  it('shows the requested tab when the detail has it, else keeps the one shown, else the first', () => {
    expect(pickModelTab(['a', 'b'], 'b', 'a')).toBe('b')
    expect(pickModelTab(['a', 'b'], 'x', 'b')).toBe('b')
    expect(pickModelTab(['a', 'b'], null, 'x')).toBe('a')
    expect(pickModelTab([], 'a', 'a')).toBeNull()
  })
})

describe('decision figures', () => {
  // Plan item 20: one text for what a decision read against its line, shared by the panel's cell, the list's card and
  // the opened decision. The first-output line is measured against the tolerance that decision was taken with.
  it('reads each line\'s figure against its line as one text; the first output against the decision\'s own tolerance', () => {
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 9, tolerance: 0.2 }) }))).toBe('22.5 / 20')
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, tolerance: 0.175 }) }))).toBe('22.5 / 17.5')
    // A record from before the tolerance was kept: unknown, never a bar worked out (no peers' line, no "two times").
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, z: 0.103, prior_mu: 0.146 }) }))).toBe('22.5 / —')
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, tolerance: null }) }))).toBe('22.5 / —')
    // The share and the seconds are never written together.
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 9, tolerance: 0.2 }) }))).not.toMatch(/8|秒|s/)
    expect(basisReading(basis('error', { error: errorEvidence({ failed: 6, padded: 80, limit: 0.025 }) }))).toBe('7.5 / 2.5')
    expect(basisReading(basis('cache', { cache: cacheEvidence({ ratio: 0.7623, min_ratio: 0.86 }) }))).toBe('76.2 / 86')
    // Nothing counted: the share is a dash, the line still drawn.
    expect(basisReading(basis('ttfo', { ttft: ttfoEvidence({ n: 0, k: 0, tolerance: 0.2 }) }))).toBe('— / 20')
    expect(basisReading(basis('cache', { cache: cacheEvidence({ turns: 0, min_ratio: 0.8 }) }))).toBe('— / 80')
    // No basis, or none of its own line's evidence: nothing to read.
    expect(basisReading(null)).toBeNull()
    expect(basisReading(basis('cache', { cache: undefined }))).toBeNull()
  })

  // Astra review of fe, must 1: rounding never puts a reading on its line or across it. Below stays below (a cache
  // line falls short only below its minimum), above stays above (errors and first output fail only above theirs),
  // equal stays equal; a reading away from its line keeps its usual figure.
  it('keeps a reading on its own side of the line when rounded, and its usual figure away from the line', () => {
    const cache = (ratio: number, min = 0.8) => basisReading(basis('cache', { cache: cacheEvidence({ ratio, min_ratio: min }) }))
    const ttfo = (k: number, n: number) => basisReading(basis('ttfo', { ttft: ttfoEvidence({ k, n, tolerance: 0.2 }) }))
    const error = (failed: number, padded: number) => basisReading(basis('error', { error: errorEvidence({ failed, padded, limit: 0.05 }) }))
    expect([cache(0.7999), cache(0.80001), cache(0.8)]).toEqual(['79.9 / 80', '80.1 / 80', '80.0 / 80'])
    expect([ttfo(501, 2500), ttfo(499, 2500), ttfo(500, 2500)]).toEqual(['20.1 / 20', '19.9 / 20', '20.0 / 20'])
    expect([error(501, 10000), error(499, 10000), error(500, 10000)]).toEqual(['5.1 / 5', '4.9 / 5', '5.0 / 5'])
    // Away from the line: rounded as usual (76.66 is 76.7, not cut to 76.6), no extra decimals.
    expect([cache(0.7666, 0.86), cache(0.9149, 0.86), ttfo(27, 40)]).toEqual(['76.7 / 86', '91.5 / 86', '67.5 / 20'])
    // Opus review of fe, suggestion 1: the failure share is over the padded count the rule judges on (missing outcomes
    // up to 50 count as successes), not over the clear outcomes: 6 of 30 clear is 6 / 50.
    expect(basisReading(basis('error', { error: errorEvidence({ clear: 30, unknown: 0, padded: 50, failed: 6, limit: 0.05 }) }))).toBe('12.0 / 5')
    // A line set to two decimals is shown so; equal to it, the reading reads the same.
    expect(basisReading(basis('error', { error: errorEvidence({ failed: 225, padded: 10000, limit: 0.0225 }) }))).toBe('2.25 / 2.25')
    expect(basisReading(basis('error', { error: errorEvidence({ failed: 226, padded: 10000, limit: 0.0225 }) }))).toBe('2.3 / 2.25')
    // A line finer than its usual figure: rounded away from it first, then with the decimals that keep them apart.
    expect(cache(0.071994, 0.071996)).toBe('7.1 / 7.2')
    expect(cache(0.071998, 0.071996)).toBe('7.1998 / 7.1996')
  })

  it('dates the evidence from the later of its cut and its window\'s start to the window\'s end, only from the record', () => {
    const local = (h: number, m: number) => new Date(2026, 10, 25, h, m).toISOString()
    const windowed = { window_from: local(12, 45), window_to: local(13, 15) }
    expect(basisPeriod(basis('cache', { cache: cacheEvidence(windowed) }))).toBe('12:45–13:15')
    expect(basisPeriod(basis('cache', { cut: local(13, 5), cache: cacheEvidence(windowed) }))).toBe('13:05–13:15')
    expect(basisPeriod(basis('cache', { cut: local(9, 0), cache: cacheEvidence(windowed) }))).toBe('12:45–13:15')
    // The same for the other two lines, from their own evidence.
    expect(basisPeriod(basis('error', { cut: local(12, 30), error: errorEvidence({ window_from: local(12, 15), window_to: local(13, 15) }) }))).toBe('12:30–13:15')
    expect(basisPeriod(basis('ttfo', { ttft: ttfoEvidence({ window_from: local(9, 15), window_to: local(13, 15) }) }))).toBe('09:15–13:15')
    // Without its window (older records): no period, even with a cut; never a window worked out here.
    expect(basisPeriod(basis('ttfo', { cut: local(13, 5) }))).toBeNull()
    expect(basisPeriod(basis('cache', { cut: local(13, 5), cache: cacheEvidence({ window_from: local(12, 45) }) }))).toBeNull()
    expect(basisPeriod(basis('error', { cut: local(13, 5), error: errorEvidence({ window_to: local(13, 15) }) }))).toBeNull()
    expect(basisPeriod(null)).toBeNull()
  })

  it('puts the period, the reading and the units counted together; what a record lacks is left out', () => {
    expect(basisFigures(basis('cache', { cache: cacheEvidence({ ratio: 0.781, min_ratio: 0.85 }), samples_local: 11, samples_total: 30 }))).toEqual({
      period: null, reading: '78.1 / 85', samples: [{ label: 'samples', value: '30' }, { label: 'local', value: '11' }]
    })
    expect(basisFigures(basis('ttfo', { ttft: undefined, samples_local: null, samples_total: null }))).toEqual({ period: null, reading: null, samples: [] })
    expect(evidenceCauses('ttfo', ttfoEvidence({ causes: ['mass_insufficient'] }))).toEqual(['mass_insufficient'])
    expect(evidenceCauses('error', errorEvidence())).toEqual([])
  })

  // A line on trial: its latest assessment, never the reading it went on trial with.
  it('reads a trial\'s latest assessment: passing, what it lacks against what it needs, or unknown', () => {
    const t = (key: string, values?: Record<string, unknown>) => (values ? `${key}:${JSON.stringify(values)}` : key)
    const latest = (patch: Partial<BackendLastEval>): BackendLastEval =>
      ({ at: '2026-11-25T13:15:00Z', since: '2026-11-25T13:10:00Z', count: 1, acted: false, action: '', reason: 'trial_open', causes: [], have: null, need: null, ...patch })
    expect(trialNote(latest({ reason: 'within' }), 'cache', t)).toEqual({ text: 'admin.schedQuality.verdict.pass', pass: true })
    expect(trialNote(latest({ causes: ['turns_insufficient'], have: 12, need: 40 }), 'cache', t))
      .toEqual({ text: 'admin.schedQuality.panel.lacksUnit.turns:{"have":12,"need":40}', pass: false })
    // A weighted amount just short of what is needed never reads as enough.
    expect(trialNote(latest({ causes: ['mass_insufficient'], have: 19.96, need: 20 }), 'ttfo', t).text).toBe('admin.schedQuality.panel.lacksUnit.samples:{"have":19,"need":20}')
    // The pair without a cause to tell what it counts: the counts alone.
    expect(trialNote(latest({ have: 2, need: 3 }), 'ttfo', t).text).toBe('admin.schedQuality.panel.lacks:{"have":2,"need":3}')
    expect(trialNote(latest({}), 'ttfo', t)).toEqual({ text: 'admin.schedQuality.verdict.unknown', pass: false })
    // Not assessed on trial yet: the latest is the review that opened it.
    expect(trialNote(latest({ acted: true, action: 'trial', reason: 'within' }), 'ttfo', t)).toEqual({ text: '—', pass: false })
    expect(trialNote(null, 'ttfo', t)).toEqual({ text: '—', pass: false })
  })

  it('writes each unit in both languages with the counts held together, and the bare counts without one', () => {
    const texts = (messages: unknown, locale: string) => {
      const { t } = createI18n({ legacy: false, locale, messages: { [locale]: messages } as never }).global
      return [null, ...LACK_UNITS].map((unit) => lacksText(t as never, unit, 2, 3))
    }
    expect(texts(zh, 'zh')).toEqual(['2\u00a0/\u00a03', '样本 2\u00a0/\u00a03', '会话 2\u00a0/\u00a03', '慢会话 2\u00a0/\u00a03', '轮 2\u00a0/\u00a03', '低会话 2\u00a0/\u00a03'])
    expect(texts(en, 'en')).toEqual(['2\u00a0of\u00a03', '2\u00a0of\u00a03 samples', '2\u00a0of\u00a03 sessions', '2\u00a0of\u00a03 slow sessions', '2\u00a0of\u00a03 turns', '2\u00a0of\u00a03 low sessions'])
  })

  // Opus review of fe, must 1: the have / need of an assessment are what held it up, not always samples. The unit is
  // picked as the backend picks the pair (assessment.go: classify, classifyTTFT, consider: the first own-short cause).
  it('names what the have / need count, in the order the backend picks them, and nothing it cannot tell', () => {
    // Each cause on its own.
    expect([
      lackUnit('ttfo', ['mass_insufficient']), lackUnit('ttfo', ['no_samples', 'mass_insufficient']), lackUnit('ttfo', ['sessions_insufficient']),
      lackUnit('ttfo', ['slow_sessions_insufficient']), lackUnit('cache', ['turns_insufficient']), lackUnit('cache', ['low_sessions_insufficient']),
      lackUnit('error', ['clear_insufficient'])
    ]).toEqual(['samples', 'samples', 'sessions', 'slowSessions', 'turns', 'lowSessions', 'samples'])
    // Two at once: the one the backend keeps the pair of (weight before sessions before slow sessions; turns first).
    expect(lackUnit('ttfo', ['mass_insufficient', 'sessions_insufficient', 'slow_sessions_insufficient'])).toBe('samples')
    expect(lackUnit('ttfo', ['sessions_insufficient', 'slow_sessions_insufficient'])).toBe('sessions')
    expect(lackUnit('cache', ['turns_insufficient', 'low_sessions_insufficient'])).toBe('turns')
    // Slow sessions do not hold it up while the share is not confirmed above the tolerance (classifyTTFT skips them).
    expect(lackUnit('ttfo', ['slow_sessions_insufficient', 'absolute_unconfirmed'])).toBeNull()
    // Causes that hold nothing up alongside one that does: the one that does.
    expect(lackUnit('error', ['bounds_unconfirmed', 'recovery_over_limit', 'clear_insufficient'])).toBe('samples')
    expect(lackUnit('ttfo', ['sessions_insufficient', 'absolute_unconfirmed'])).toBe('sessions')
    // No cause, one the line does not know (or another line's), or the previous rules' peers: no unit word.
    expect([
      lackUnit('ttfo', []), lackUnit('ttfo', ['other']), lackUnit('ttfo', ['mass_insufficient', 'something_new']),
      lackUnit('cache', ['clear_insufficient']), lackUnit('ttfo', ['peer_known_insufficient', 'mass_insufficient']), lackUnit('error', ['bounds_unconfirmed'])
    ]).toEqual([null, null, null, null, null, null])
  })
})

describe('decision records', () => {
  const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
    id, at: '2026-11-16T10:25:10Z', group_id: 1, model: 'a', line: 'ttfo', action: 'enter', level: 1, until: '2026-11-16T11:05:00Z', ...patch
  })

  it('reads one moment\'s action on a model\'s line across groups as one entry, each group once and every record kept', () => {
    const merged = mergeDecisions([
      record(9, { group_id: 2 }),
      record(8, { group_id: 1, at: '2026-11-16T10:25:40Z' }),
      record(7, { group_id: 2 }),
      record(6, { model: 'b' }),
      record(5, { at: '2026-11-16T10:20:00Z' }),
      record(4, { action: 'renew' })
    ])
    expect(merged.map((entry) => [entry.items.map((item) => item.id), entry.groups])).toEqual([
      [[9, 8, 7], [2, 1]],
      [[6], [1]],
      [[5], [1]],
      [[4], [1]]
    ])
    // Another end is another entry.
    expect(mergeDecisions([record(2), record(1, { group_id: 2, until: '2026-11-16T11:25:00Z' })])).toHaveLength(2)
  })

  // Plan item 18: an entry is titled from its first record, so around the switch to 4.0 records of another rule version,
  // or that ended a trial for another reason, are another entry even in the same minute.
  it('keeps records of another rule version or another release reason apart in the same minute', () => {
    const ended = (rule_version: string, release_reason?: string) => ({ action: 'trial_timeout', until: null, basis: basis('ttfo', { rule_version, release_reason }) })
    const merged = mergeDecisions([
      record(7, { group_id: 1, ...ended('4.0', 'deadline') }),
      record(6, { group_id: 2, ...ended('4.0', 'deadline') }),
      record(5, { group_id: 3, ...ended('3.1', 'deadline') }),
      record(4, { group_id: 4, ...ended('3.1', 'no_samples') }),
      record(3, { group_id: 5, ...ended('4.0', 'good') }),
      record(2, { group_id: 6, ...ended('4.0') }),
      record(1, { group_id: 7, action: 'trial_timeout', until: null, basis: null })
    ])
    expect(merged.map((entry) => entry.items.map((item) => item.id))).toEqual([[7, 6], [5], [4], [3], [2], [1]])
  })

  // Astra 2: ends within the minute shown merge, and each record keeps its own end (and so its evidence).
  it('keeps each record\'s own end when ends share the minute shown', () => {
    const merged = mergeDecisions([record(2, { until: '2026-11-16T11:05:01Z' }), record(1, { group_id: 2, until: '2026-11-16T11:05:59Z' })])
    expect(merged).toHaveLength(1)
    expect(merged[0].items.map((item) => [item.group_id, item.until])).toEqual([[1, '2026-11-16T11:05:01Z'], [2, '2026-11-16T11:05:59Z']])
  })
})
