/**
 * A group's alias: the short text on its pill (the accounts page's groups column, the quality panel, the group picker,
 * the groups page) and the length rule its input follows (spec 6, 7a).
 */

const HAN = /\p{Script=Han}/u
const ALNUM = /[A-Za-z0-9]/

/**
 * A group's pill text: its alias, or when it has none, the first two Chinese characters of its name, or else its
 * first three letters and digits ("Gemini Ultra" → "Gem").
 */
export function groupPillText(group: { name: string; alias?: string | null }): string {
  const alias = group.alias?.trim()
  if (alias) return alias
  const chars = Array.from(group.name ?? '')
  const han = chars.filter((char) => HAN.test(char))
  if (han.length) return han.slice(0, 2).join('')
  const alnum = chars.filter((char) => ALNUM.test(char))
  if (alnum.length) return alnum.slice(0, 3).join('')
  return chars.slice(0, 2).join('')
}

/** Text made of ASCII only is set at 11px so three letters fit the pill. */
export const isLatinPillText = (text: string) => text.length > 0 && Array.from(text).every((char) => char.charCodeAt(0) < 0x80)

/**
 * East Asian wide and fullwidth characters (Unicode UAX #11, W and F) as far as an alias goes: Han, kana, Hangul
 * syllables, CJK punctuation, fullwidth forms and emoji; halfwidth forms and conjoining Hangul jamo are narrow. The
 * backend (service/group_alias.go) reads the full Unicode width table; the two agree on every case of its tests.
 */
const WIDE = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Emoji_Presentation}\u3000-\u303e\uff01-\uff60\uffe0-\uffe6]/u
const NARROW = /[\u1160-\u11ff\ud7b0-\ud7ff\uff61-\uffdc]/u

/**
 * The alias fits its pill (spec 7a): a wide character counts 1, any other 2/3, at most 2 in all, so 2 Chinese
 * characters or 3 letters and digits. Counted in thirds, as the backend does: 3 and 2, at most 6. Surrounding spaces
 * do not count (both sides trim them).
 */
export function groupAliasFits(alias: string): boolean {
  let thirds = 0
  for (const char of alias.trim()) thirds += WIDE.test(char) && !NARROW.test(char) ? 3 : 2
  return thirds <= 6
}
