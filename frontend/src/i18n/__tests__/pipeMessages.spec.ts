import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '../locales/en'
import zh from '../locales/zh'

const pluralKeys = new Set([
  'announcements.newCount',
  'batchImage.list.selectedJobs',
  'batchImage.list.expandChildren',
  'batchImage.list.childCount',
  'batchImage.create.outputCountOption',
  'batchImage.create.referenceCount'
])

function pipeMessages(node: unknown, prefix = ''): Array<[string, string]> {
  if (typeof node === 'string') return node.includes('|') ? [[prefix, node]] : []
  if (!node || typeof node !== 'object') return []
  return Object.entries(node).flatMap(([key, value]) => pipeMessages(value, prefix ? `${prefix}.${key}` : key))
}

describe.each([
  ['en', en],
  ['zh', zh]
] as const)('%s messages containing pipes', (locale, messages) => {
  const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages }, warnHtmlMessage: false })
  for (const [key, message] of pipeMessages(messages)) {
    it(`renders the complete message for ${key}`, () => {
      if (pluralKeys.has(key)) {
        const branches = message.split('|').map(part => part.trim())
        expect(branches).toHaveLength(2)
        for (const count of [0, 1, 2]) {
          const expected = branches[count === 1 ? 0 : 1].replace(/\{(?:n|count)\}/g, String(count))
          expect(i18n.global.t(key, { n: count, count }, count)).toBe(expected)
        }
      } else {
        const expected = message.replaceAll("{'|'}", '|').replaceAll('{buffer}', '17')
        expect(i18n.global.t(key, { buffer: 17 })).toBe(expected)
      }
    })
  }
})
