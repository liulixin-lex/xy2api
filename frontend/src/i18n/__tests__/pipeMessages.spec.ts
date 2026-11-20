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
  'batchImage.create.referenceCount',
  'admin.accounts.table.tip.count',
  'admin.accounts.table.tip.failed',
  'admin.accounts.table.tip.slowTimeouts',
  'admin.groups.profitControl.floorBelow',
  'admin.groups.profitControl.pinnedCount',
  'admin.accounts.table.tip.turns',
  'admin.schedQuality.board.summaryWaitRequests',
  'admin.schedQuality.perf.total',
  'admin.schedQuality.perf.turns',
  'admin.schedQuality.perf.times',
  'admin.schedQuality.perf.count.times',
  'admin.schedQuality.perf.count.turns',
  'admin.schedQuality.perf.count.sessions',
  'admin.schedQuality.perf.count.attempts',
  'admin.schedQuality.perf.count.firstSends',
  'admin.schedQuality.perf.count.comparable',
  'admin.schedQuality.perf.count.scoredAccounts',
  'usage.e2eFirstOutputSamples',
  'usage.e2eFirstOutputP50Missing',
  'admin.ops.e2eFirstOutput.samples'
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
          // {n} or {count} is the count; any other number in the message (failed of n) reads 7.
          const value = (name: string) => (name === 'n' || name === 'count' ? count : 7)
          const params = Object.fromEntries(Array.from(message.matchAll(/\{(\w+)\}/g), ([, name]) => [name, value(name)]))
          const expected = branches[count === 1 ? 0 : 1].replace(/\{(\w+)\}/g, (_, name: string) => String(value(name)))
          expect(i18n.global.t(key, params, count)).toBe(expected)
        }
      } else {
        const expected = message.replaceAll("{'|'}", '|').replaceAll('{buffer}', '17')
        expect(i18n.global.t(key, { buffer: 17 })).toBe(expected)
      }
    })
  }
})
