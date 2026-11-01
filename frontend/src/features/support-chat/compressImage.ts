import { SUPPORT_CHAT_IMAGE_MAX_BYTES } from '@/api/supportChat'

/**
 * Client-side image compression for support chat uploads.
 *
 * Every image is re-drawn through a canvas, which drops EXIF and other metadata
 * and applies the EXIF orientation. Screenshots that stay small as PNG keep their
 * sharp text; everything else becomes WebP (JPEG on a white background when the
 * browser cannot encode WebP). The plan is a pure function so it can be tested
 * without a canvas; the codecs are injectable for the same reason.
 */

export const COMPRESS_MAX_EDGE = 2560
export const COMPRESS_TARGET_BYTES = 1024 * 1024
export const COMPRESS_HARD_MAX_BYTES = SUPPORT_CHAT_IMAGE_MAX_BYTES
const LOSSY_QUALITIES = [0.85, 0.75, 0.65]
const SCALE_FACTOR = 0.8
const MAX_SCALE_STEPS = 10

export type CompressedImageType = 'image/png' | 'image/webp' | 'image/jpeg'

export interface CompressionStep {
  type: CompressedImageType
  quality?: number
  width: number
  height: number
}

export interface CompressionPlanInput {
  sourceType: string
  width: number
  height: number
  /** False once the browser has shown it cannot encode WebP. */
  webp?: boolean
}

/** Output size after the long-edge cap, never upscaled. */
export function fitWithinEdge(width: number, height: number, maxEdge = COMPRESS_MAX_EDGE): { width: number; height: number } {
  const longEdge = Math.max(width, height)
  if (longEdge <= maxEdge) return { width: Math.max(1, Math.round(width)), height: Math.max(1, Math.round(height)) }
  const ratio = maxEdge / longEdge
  return { width: Math.max(1, Math.round(width * ratio)), height: Math.max(1, Math.round(height * ratio)) }
}

/**
 * Ordered encode attempts; the first result within COMPRESS_TARGET_BYTES wins.
 * PNG sources try a PNG re-encode first, then lossy steps lower the quality
 * (0.85 → 0.75 → 0.65) and finally shrink the image by ×0.8 per step.
 */
export function planCompressionSteps(input: CompressionPlanInput): CompressionStep[] {
  const lossy: CompressedImageType = input.webp === false ? 'image/jpeg' : 'image/webp'
  const base = fitWithinEdge(input.width, input.height)
  const steps: CompressionStep[] = []
  if (input.sourceType === 'image/png') steps.push({ type: 'image/png', ...base })
  for (const quality of LOSSY_QUALITIES) steps.push({ type: lossy, quality, ...base })
  const lastQuality = LOSSY_QUALITIES[LOSSY_QUALITIES.length - 1]
  let scale = 1
  for (let i = 0; i < MAX_SCALE_STEPS; i++) {
    scale *= SCALE_FACTOR
    const width = Math.max(1, Math.round(base.width * scale))
    const height = Math.max(1, Math.round(base.height * scale))
    if (Math.max(width, height) < 64) break
    steps.push({ type: lossy, quality: lastQuality, width, height })
  }
  return steps
}

export interface DecodedImage {
  width: number
  height: number
  source: CanvasImageSource
  close?: () => void
}

export interface EncodeOptions {
  type: CompressedImageType
  quality?: number
  width: number
  height: number
  /** Painted under the image; JPEG has no alpha. */
  background?: string
}

export type ImageDecoder = (file: Blob) => Promise<DecodedImage>
export type ImageEncoder = (image: DecodedImage, options: EncodeOptions) => Promise<Blob>

export interface CompressedImage {
  blob: Blob
  type: string
  width: number
  height: number
  originalSize: number
  size: number
}

export type ImageCompressionErrorCode = 'not_image' | 'decode' | 'too_large'

export class ImageCompressionError extends Error {
  constructor(readonly code: ImageCompressionErrorCode) {
    super(`image compression failed: ${code}`)
    this.name = 'ImageCompressionError'
  }
}

function loadWithImageElement(file: Blob): Promise<DecodedImage> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => {
      resolve({ width: img.naturalWidth, height: img.naturalHeight, source: img, close: () => URL.revokeObjectURL(url) })
    }
    img.onerror = () => {
      URL.revokeObjectURL(url)
      reject(new ImageCompressionError('decode'))
    }
    img.src = url
  })
}

/** createImageBitmap honours EXIF orientation; older browsers fall back to <img>. GIF decodes to its first frame. */
export const defaultDecode: ImageDecoder = async (file) => {
  if (typeof createImageBitmap === 'function') {
    try {
      const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' })
      return { width: bitmap.width, height: bitmap.height, source: bitmap, close: () => bitmap.close() }
    } catch {
      // Fall through to <img>, which some browsers decode more formats with.
    }
  }
  return loadWithImageElement(file)
}

export const defaultEncode: ImageEncoder = (image, options) => new Promise((resolve, reject) => {
  const canvas = document.createElement('canvas')
  canvas.width = options.width
  canvas.height = options.height
  const context = canvas.getContext('2d')
  if (!context) {
    reject(new ImageCompressionError('decode'))
    return
  }
  if (options.background) {
    context.fillStyle = options.background
    context.fillRect(0, 0, options.width, options.height)
  }
  context.drawImage(image.source, 0, 0, options.width, options.height)
  canvas.toBlob((blob) => {
    if (blob) resolve(blob)
    else reject(new ImageCompressionError('decode'))
  }, options.type, options.quality)
})

/**
 * Compresses one picked, pasted or dropped image for upload.
 * Throws ImageCompressionError: not_image, decode (HEIC and other formats the
 * browser cannot read) or too_large (nothing fit in 1.5 MiB).
 */
export async function compressImage(
  file: Blob,
  codecs: { decode?: ImageDecoder; encode?: ImageEncoder } = {},
): Promise<CompressedImage> {
  if (!file.type.startsWith('image/')) throw new ImageCompressionError('not_image')
  const decode = codecs.decode ?? defaultDecode
  const encode = codecs.encode ?? defaultEncode
  let image: DecodedImage
  try {
    image = await decode(file)
  } catch {
    throw new ImageCompressionError('decode')
  }
  try {
    if (!image.width || !image.height) throw new ImageCompressionError('decode')
    let webp = true
    let steps = planCompressionSteps({ sourceType: file.type, width: image.width, height: image.height, webp })
    let best: CompressedImage | null = null
    for (let index = 0; index < steps.length; index++) {
      const step = steps[index]
      const blob = await encode(image, {
        type: step.type,
        quality: step.quality,
        width: step.width,
        height: step.height,
        background: step.type === 'image/jpeg' ? '#ffffff' : undefined,
      })
      // Browsers without a WebP encoder silently return PNG; switch the rest of the plan to JPEG.
      if (step.type === 'image/webp' && blob.type !== 'image/webp') {
        webp = false
        steps = planCompressionSteps({ sourceType: file.type, width: image.width, height: image.height, webp })
        index = steps.findIndex((candidate) => candidate.type === 'image/jpeg') - 1
        continue
      }
      const result: CompressedImage = { blob, type: blob.type || step.type, width: step.width, height: step.height, originalSize: file.size, size: blob.size }
      if (blob.size <= COMPRESS_TARGET_BYTES) return result
      if (!best || blob.size < best.size) best = result
    }
    if (best && best.size <= COMPRESS_HARD_MAX_BYTES) return best
    throw new ImageCompressionError('too_large')
  } finally {
    image.close?.()
  }
}

/** 「3.2 MB」/「280 KB」 for the preview chip. */
export function formatImageBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${Math.max(1, Math.round(bytes / 1024))} KB`
}
