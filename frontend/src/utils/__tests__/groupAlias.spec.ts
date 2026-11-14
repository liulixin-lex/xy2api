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
  // The cases of the backend's TestNormalizeGroupAlias (control characters aside: only the backend checks those).
  it.each([
    ['', true], ['  中文 ', true], ['ABC', true], ['123', true], ['A稳', true], ['稳A', true], ['ＡＢ', true],
    ['ｶﾀｶ', true], ['αβγ', true], ['😀😀', true],
    ['AB12', false], ['AB稳', false], ['中AB', false], ['ＡＢＣ', false], ['中文字', false], ['ｶﾀｶﾅ', false],
    ['αβγδ', false], ['中文A', false]
  ])('%s fits: %s', (alias, fits) => {
    expect(groupAliasFits(alias)).toBe(fits)
  })
})
