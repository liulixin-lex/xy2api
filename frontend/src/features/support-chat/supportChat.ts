import { extractI18nErrorMessage } from '@/utils/apiError'
import { ImageCompressionError } from './compressImage'

type Translate = (key: string, params?: Record<string, unknown>) => string

/** Localized message for a failed chat request, using the reason code when known. */
export function supportChatErrorMessage(err: unknown, t: Translate, fallbackKey = 'supportChat.actionFailed'): string {
  return extractI18nErrorMessage(err, t, 'supportChat.errors', t(fallbackKey))
}

/** Localized message for an image that could not be prepared for upload. */
export function supportChatImageErrorMessage(err: unknown, t: Translate): string {
  if (err instanceof ImageCompressionError) return t(`supportChat.image.errors.${err.code}`)
  return t('supportChat.image.errors.decode')
}

/** HTTP status of a rejected API call (the client keeps it on the rejection object). */
export function supportChatErrorStatus(err: unknown): number {
  if (err && typeof err === 'object' && typeof (err as { status?: unknown }).status === 'number') {
    return (err as { status: number }).status
  }
  return 0
}

/** Whole minutes since an ISO time, never negative. */
export function minutesSince(iso: string | null | undefined, now = Date.now()): number {
  if (!iso) return 0
  const time = Date.parse(iso)
  if (Number.isNaN(time)) return 0
  return Math.max(0, Math.floor((now - time) / 60_000))
}
