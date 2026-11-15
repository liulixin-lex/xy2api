import { describe, expect, it } from 'vitest'

import { groupAliasFits, groupPillText, isLatinPillText } from '../groupAlias'

describe('pill text', () => {
  it('uses the alias when there is one', () => {
    expect(groupPillText({ name: 'OpenAI 主力池', alias: '主力' })).toBe('主力')
  })

  it('takes the first two Chinese characters, else the first three letters and digits', () => {
    expect(groupPillText({ name: 'GPT 主力 备用池', alias: '' })).toBe('主力')
    expect(groupPillText({ name: 'Gemini Ultra' })).toBe('Gem')
    expect(groupPillText({ name: 'g-4 pro' })).toBe('g4p')
    expect(groupPillText({ name: '  ', alias: '  ' })).toBe('  ')
  })

  it('sets text made of ASCII only at the small size', () => {
    expect(isLatinPillText('Gem')).toBe(true)
    expect(isLatinPillText('主力')).toBe(false)
    expect(isLatinPillText('A主')).toBe(false)
  })
})

describe('alias length', () => {
  // The cases of the backend's TestNormalizeGroupAlias (control characters aside: only the backend checks those), and
  // those where the two used to differ (7j-2): the prolonged sound mark and Bopomofo are wide, a flag's two letters
  // narrow, and a byte-order mark is not trimmed.
  it.each([
    ['', true], ['  中文 ', true], ['ABC', true], ['123', true], ['A稳', true], ['稳A', true], ['ＡＢ', true],
    ['ｶﾀｶ', true], ['αβγ', true], ['😀😀', true],
    ['AB12', false], ['AB稳', false], ['中AB', false], ['ＡＢＣ', false], ['中文字', false], ['ｶﾀｶﾅ', false],
    ['αβγδ', false], ['中文A', false],
    ['AーB', false], ['ㄅㄆㄇ', false], ['🇨🇳A', true], ['中文\uFEFF', false]
  ])('%s fits: %s', (alias, fits) => {
    expect(groupAliasFits(alias)).toBe(fits)
  })

  it('counts wide the CJK symbols the backend does: brackets, trigrams, strokes, Kanbun, hexagrams, vertical and small forms, squared symbols (7m-4)', () => {
    const firsts = [0x2329, 0x2630, 0x268a, 0x2ff0, 0x3190, 0x31c0, 0x31ef, 0x4dc0, 0xfe10, 0xfe30, 0xfe6b, 0x1f202, 0x1f210, 0x1f237, 0x1f23b, 0x1f240, 0x1f260]
    // A wide character and two letters make 7 thirds, one too many; a narrow one would make 6.
    expect(firsts.filter((cp) => groupAliasFits(`${String.fromCodePoint(cp)}AB`))).toEqual([])
  })
})
