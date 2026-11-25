import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// The quality panel, the group board and the decision records build these keys from values the backend sends
// (identities, placements, layers, actions, unknown causes, evidence fields), so the static key check cannot see
// them. Every value of the contract (blueprint section 4, machine Action list, rule B's unknown reasons) has copy in
// both languages, and so has every label of a decision's figures.
const VALUES: Record<string, string[]> = {
  'admin.schedQuality.identity': ['pass', 'unknown', 'trial', 'rear'],
  'admin.schedQuality.placement': ['front', 'trial', 'rear', 'exempt', 'stopped'],
  'admin.schedQuality.layer': ['front', 'trial', 'trial_full', 'rear', 'stopped'],
  'admin.schedQuality.decisions.action': ['enter', 'renew', 'release', 'trial', 'trial_pass', 'trial_fail', 'trial_timeout', 'reset', 'relax', 'clear'],
  'admin.schedQuality.evidence.causes': [
    'no_peers', 'all_peers_rear', 'peer_known_insufficient', 'no_samples', 'mass_insufficient', 'sessions_insufficient',
    'slow_sessions_insufficient', 'absolute_unconfirmed', 'relative_unconfirmed', 'worst_mean_above_tolerance', 'relative_veto'
  ],
  'admin.schedQuality.evidence': ['over', 'bar', 'peers', 'failed', 'limit', 'hit', 'min', 'samples', 'local'],
  'admin.schedQuality.decisions.timeout.seats': ['full', 'not_observed', 'unknown'],
  'admin.schedQuality.ttftRule': ['confidence', 'tolerance', 'confidenceHint', 'toleranceHint'],
  'admin.schedQuality.ttftRule.legal': ['confidence', 'tolerance'],
  'admin.schedQuality.reject': ['profit_floor'],
  'admin.schedQuality.kind': ['capability_marked']
}

const lookup = (messages: unknown, path: string): unknown =>
  path.split('.').reduce<unknown>((node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined), messages)

// The board's two rate-range exclusions read as the floor and the ceiling ("不用 · 低于下限 / 高于上限").
it('names the rate-range exclusions as below the floor and above the ceiling', () => {
  const words = (messages: unknown) => ['profit_floor', 'profit_threshold'].map((key) => lookup(messages, `admin.schedQuality.reject.${key}`))
  expect([words(zh), words(en)]).toEqual([['低于下限', '高于上限'], ['Below the rate floor', 'Above the rate ceiling']])
})

// With the switch off the engine still judges and only selection ignores the layers; a group with its gate off is not
// judged at all. The words say exactly that.
it('says what each gate-off really stops', () => {
  const words = (messages: unknown) => ['gateOffGlobal', 'gateOffGroup'].map((key) => String(lookup(messages, `admin.schedQuality.identity.${key}`)))
  const [zhGlobal, zhGroup] = words(zh)
  const [enGlobal, enGroup] = words(en)
  expect([zhGlobal.includes('判定照常进行'), zhGlobal.includes('不做判定'), zhGroup.includes('不做判定')]).toEqual([true, false, true])
  expect([enGlobal.includes('judging goes on'), enGlobal.includes('nothing is judged'), enGroup.includes('judged')]).toEqual([true, false, true])
})

// N3: the no-data note takes each reason an unknown line can have; none of them may put brackets inside brackets.
it('writes the no-data note without brackets inside brackets, whatever the reason', () => {
  const depth = (text: string) => {
    let deepest = 0
    let open = 0
    for (const char of text) {
      if ('(（'.includes(char)) deepest = Math.max(deepest, ++open)
      if (')）'.includes(char)) open--
    }
    return deepest
  }
  const reasons = ['schedQuality.identity.unknownWhy', 'schedQuality.identity.gateOffGlobal', 'schedQuality.identity.gateOffGroup', 'schedQuality.tip.stale']
  const notes = [zh, en].flatMap((messages) =>
    reasons.map((key) => String(lookup(messages, 'admin.schedQuality.panel.noData')).replace('{why}', String(lookup(messages, `admin.${key}`))))
  )
  expect(notes.filter((note) => depth(note) > 1)).toEqual([])
})

describe.each([['zh', zh], ['en', en]] as const)('quality gate copy (%s)', (_locale, messages) => {
  it('names every identity, place, layer, action, unknown cause and evidence field the backend sends', () => {
    const missing = Object.entries(VALUES).flatMap(([prefix, values]) =>
      values.map((value) => `${prefix}.${value}`).filter((key) => {
        const text = lookup(messages, key)
        return typeof text !== 'string' || text.trim() === ''
      })
    )
    expect(missing).toEqual([])
  })

  // One word for the rear wherever it shows: the list's filter and dot, the panel and the board.
  it('names the rear the same in the filter, the dot, the panel and the board', () => {
    const words = [
      'admin.accounts.table.quality.rear', 'admin.accounts.table.dotStates.back',
      'admin.schedQuality.identity.rear', 'admin.schedQuality.placement.rear', 'admin.schedQuality.layer.rear'
    ].map((key) => lookup(messages, key))
    expect(new Set(words).size).toBe(1)
  })

  // Plan item 18: the rules in force do not compare with peers; the first-output settings say nothing of them.
  it('keeps peers out of the first-output settings', () => {
    const texts: string[] = []
    const walk = (node: unknown) => {
      if (typeof node === 'string') texts.push(node)
      else if (node && typeof node === 'object') Object.values(node).forEach(walk)
    }
    walk(lookup(messages, 'admin.schedQuality.ttftRule'))
    expect(texts.filter((text) => /同伴|前排|peer|front accounts/i.test(text))).toEqual([])
    expect(lookup(messages, 'admin.schedQuality.ttftRule.peer_margin')).toBeUndefined()
  })

  it('puts the first-output limit into its figure label', () => {
    expect(String(lookup(messages, 'admin.schedQuality.evidence.over'))).toContain('{s}')
  })

  // 前端修整 (package C): the panel's figure headings fit their 56px columns (12px text), and in Chinese the error line
  // is 报错 wherever the quality copy names it.
  it('keeps the panel\'s figure headings within their columns and names the error line one way', () => {
    const headings = Object.values(lookup(messages, 'admin.schedQuality.panel.col') as Record<string, string>)
    const width = (text: string) => [...text].reduce((sum, char) => sum + (/[\u3000-\u9fff\uff00-\uffef]/.test(char) ? 12 : 7), 0)
    expect(headings.filter((text) => width(text) > 56)).toEqual([])
    if (messages === zh) {
      expect(['dim.error', 'policy.error', 'lineName.error', 'panel.col.error'].map((key) => String(lookup(messages, `admin.schedQuality.${key}`)))).toEqual([
        '报错', '报错上限', '报错线', '报错'
      ])
    }
  })
})

// Final review suggestion 5: one apostrophe in the English copy.
it('uses the straight apostrophe in the English quality copy', () => {
  const texts: string[] = []
  const walk = (node: unknown) => {
    if (typeof node === 'string') texts.push(node)
    else if (node && typeof node === 'object') Object.values(node).forEach(walk)
  }
  walk(lookup(en, 'admin.schedQuality'))
  expect(texts.filter((text) => /[‘’]/.test(text))).toEqual([])
})

// Acceptance round 2: durations read as minutes, not points ("分" next to scores); English results start alike.
it('writes durations in minutes and capitalizes every English result', () => {
  expect([lookup(zh, 'admin.schedQuality.duration.minutes'), lookup(zh, 'admin.schedQuality.duration.hoursMinutes')]).toEqual(['{m} 分钟', '{h} 小时 {m} 分钟'])
  const results = [lookup(en, 'admin.schedQuality.perf.detail.resultNone'), ...Object.values(lookup(en, 'admin.schedQuality.decisions.action') as Record<string, string>)]
  expect(results.map(String).filter((text) => text[0] !== text[0].toUpperCase())).toEqual([])
})
