import { describe, expect, it } from 'vitest'
import { isOfficialGeminiBaseUrl } from '../geminiUpstream'

describe('Gemini upstream identity', () => {
  it.each(['', 'https://generativelanguage.googleapis.com', 'https://generativelanguage.googleapis.com/', 'https://generativelanguage.googleapis.com:443'])('recognizes the official endpoint %s', (url) => {
    expect(isOfficialGeminiBaseUrl(url)).toBe(true)
  })
  it.each(['https://relay.example.com', 'https://generativelanguage.googleapis.com.evil.test', 'https://generativelanguage.googleapis.com/proxy', 'http://generativelanguage.googleapis.com', 'https://user@generativelanguage.googleapis.com', 'not a URL', 'https://generativelanguage.googleapis.com/%2f', 'https://generativelanguage.googleapis.com/a/..'])('uses relay policy for %s', (url) => {
    expect(isOfficialGeminiBaseUrl(url)).toBe(false)
  })
})
