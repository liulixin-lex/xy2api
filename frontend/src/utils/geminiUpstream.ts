export function isOfficialGeminiBaseUrl(raw: unknown): boolean {
  if (typeof raw !== 'string' || !raw.trim()) return true
  // Match the configured root without URL() normalizing dot or escaped paths.
  return /^https:\/\/generativelanguage\.googleapis\.com(?::443)?\/*$/i.test(raw.trim())
}
