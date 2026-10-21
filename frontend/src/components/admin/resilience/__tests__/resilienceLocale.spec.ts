import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// Keys built at runtime (`sections.${key}`, `reasons.${reason}`) escape the
// static locale completeness check, so the 0.4.4 ones are listed here.
const dynamicKeys = [
  ...['failure', 'stream', 'capability'].flatMap((k) => [`admin.resilience.settings.sections.${k}`, `admin.resilience.settings.sections.${k}Hint`]),
  ...['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal', 'failedFrameRetryAfter']
    .flatMap((k) => [`admin.resilience.settings.switches.${k}`, `admin.resilience.settings.switches.${k}Hint`]),
  ...['codexRetryAfterCap', 'learnTtl'].flatMap((k) => [`admin.resilience.settings.fields.${k}`, `admin.resilience.settings.fields.${k}Hint`]),
  'admin.resilience.explain.reasons.capability_stream_only'
]

function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined), messages)
}

describe('gateway resilience 0.4.4 locale keys', () => {
  it.each([['zh', zh], ['en', en]])('%s has every runtime-built key', (_, messages) => {
    const missing = dynamicKeys.filter((key) => typeof lookup(messages, key) !== 'string' || !(lookup(messages, key) as string).trim())
    expect(missing).toEqual([])
  })
})
