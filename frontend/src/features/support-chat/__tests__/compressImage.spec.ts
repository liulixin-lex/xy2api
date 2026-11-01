import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  COMPRESS_HARD_MAX_BYTES,
  COMPRESS_TARGET_BYTES,
  compressImage,
  fitWithinEdge,
  formatImageBytes,
  ImageCompressionError,
  planCompressionSteps,
  type DecodedImage,
  type EncodeOptions,
} from '../compressImage'

const MB = 1024 * 1024
const file = (type: string, size = 3 * MB) => new Blob([new Uint8Array(size)], { type })
const decoded = (width: number, height: number): DecodedImage => ({ width, height, source: {} as CanvasImageSource, close: vi.fn() })

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('planCompressionSteps', () => {
  it('caps the long edge at 2560 without upscaling', () => {
    expect(fitWithinEdge(5120, 2880)).toEqual({ width: 2560, height: 1440 })
    expect(fitWithinEdge(800, 600)).toEqual({ width: 800, height: 600 })
  })

  it('tries PNG first for PNG sources, then WebP qualities, then smaller sizes', () => {
    const steps = planCompressionSteps({ sourceType: 'image/png', width: 4000, height: 2000 })
    expect(steps[0]).toEqual({ type: 'image/png', width: 2560, height: 1280 })
    expect(steps.slice(1, 4)).toEqual([
      { type: 'image/webp', quality: 0.85, width: 2560, height: 1280 },
      { type: 'image/webp', quality: 0.75, width: 2560, height: 1280 },
      { type: 'image/webp', quality: 0.65, width: 2560, height: 1280 },
    ])
    expect(steps[4]).toEqual({ type: 'image/webp', quality: 0.65, width: 2048, height: 1024 })
    expect(steps[5]).toEqual({ type: 'image/webp', quality: 0.65, width: 1638, height: 819 })
    expect(steps.every((step) => Math.max(step.width, step.height) >= 64)).toBe(true)
  })

  it('skips the PNG step for photos and uses JPEG without WebP support', () => {
    const steps = planCompressionSteps({ sourceType: 'image/jpeg', width: 1200, height: 900, webp: false })
    expect(steps[0]).toEqual({ type: 'image/jpeg', quality: 0.85, width: 1200, height: 900 })
    expect(steps.some((step) => step.type !== 'image/jpeg')).toBe(false)
  })
})

describe('compressImage', () => {
  it('keeps a small PNG screenshot as PNG', async () => {
    const encode = vi.fn(async (_image: DecodedImage, options: EncodeOptions) => file(options.type, 400 * 1024))
    const image = decoded(1920, 1080)
    const result = await compressImage(file('image/png'), { decode: async () => image, encode })
    expect(encode).toHaveBeenCalledTimes(1)
    expect(result).toMatchObject({ type: 'image/png', width: 1920, height: 1080, originalSize: 3 * MB, size: 400 * 1024 })
    expect(image.close).toHaveBeenCalled()
  })

  it('lowers the quality, then the size, until the result fits 1 MB', async () => {
    const sizes = [5 * MB, 2 * MB, 1.5 * MB, 1.2 * MB, 900 * 1024]
    const encode = vi.fn(async (_image: DecodedImage, options: EncodeOptions) => file(options.type, sizes.shift()!))
    const result = await compressImage(file('image/png', 8 * MB), { decode: async () => decoded(3000, 3000), encode })
    expect(encode.mock.calls.map(([, options]) => [options.type, options.quality, options.width])).toEqual([
      ['image/png', undefined, 2560],
      ['image/webp', 0.85, 2560],
      ['image/webp', 0.75, 2560],
      ['image/webp', 0.65, 2560],
      ['image/webp', 0.65, 2048],
    ])
    expect(result.size).toBeLessThanOrEqual(COMPRESS_TARGET_BYTES)
    expect(result.width).toBe(2048)
  })

  it('falls back to JPEG on a white background when the browser cannot encode WebP', async () => {
    const encode = vi.fn(async (_image: DecodedImage, options: EncodeOptions) =>
      options.type === 'image/webp' ? file('image/png', 2 * MB) : file('image/jpeg', 300 * 1024))
    const result = await compressImage(file('image/jpeg'), { decode: async () => decoded(1000, 800), encode })
    expect(encode).toHaveBeenCalledTimes(2)
    expect(encode.mock.calls[1][1]).toMatchObject({ type: 'image/jpeg', quality: 0.85, background: '#ffffff' })
    expect(result.type).toBe('image/jpeg')
  })

  it('accepts the smallest result up to 1.5 MiB and refuses anything bigger', async () => {
    const fits = await compressImage(file('image/jpeg'), {
      decode: async () => decoded(80, 80),
      encode: async (_image, options) => file(options.type, 1200 * 1024),
    })
    expect(fits.size).toBe(1200 * 1024)
    expect(fits.size).toBeLessThanOrEqual(COMPRESS_HARD_MAX_BYTES)

    await expect(compressImage(file('image/jpeg'), {
      decode: async () => decoded(80, 80),
      encode: async (_image, options) => file(options.type, 2 * MB),
    })).rejects.toMatchObject({ code: 'too_large' })
  })

  it('explains images the browser cannot read', async () => {
    await expect(compressImage(file('image/heic'), { decode: async () => { throw new Error('nope') } }))
      .rejects.toEqual(new ImageCompressionError('decode'))
    await expect(compressImage(file('application/pdf'))).rejects.toMatchObject({ code: 'not_image' })
  })

  it('draws through a real canvas path with createImageBitmap (smoke)', async () => {
    const drawImage = vi.fn()
    const fillRect = vi.fn()
    const close = vi.fn()
    const createImageBitmap = vi.fn(async () => ({ width: 3200, height: 1800, close }))
    vi.stubGlobal('createImageBitmap', createImageBitmap)
    const originalCreateElement = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation(((tagName: string, options?: ElementCreationOptions) => {
      if (tagName === 'canvas') {
        return {
          width: 0,
          height: 0,
          getContext: () => ({ drawImage, fillRect, fillStyle: '' }),
          toBlob: (callback: BlobCallback, type?: string) => callback(new Blob([new Uint8Array(200 * 1024)], { type: type ?? 'image/png' })),
        } as unknown as HTMLCanvasElement
      }
      return originalCreateElement(tagName, options)
    }) as typeof document.createElement)

    const source = new Blob([new Uint8Array(4 * MB)], { type: 'image/gif' })
    const result = await compressImage(source)
    expect(createImageBitmap).toHaveBeenCalledWith(source, { imageOrientation: 'from-image' })
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 2560, 1440)
    expect(result).toMatchObject({ type: 'image/webp', width: 2560, height: 1440, originalSize: 4 * MB, size: 200 * 1024 })
    expect(close).toHaveBeenCalled()
  })

  it('formats sizes for the preview chip', () => {
    expect(`${formatImageBytes(3.2 * MB)} → ${formatImageBytes(280 * 1024)}`).toBe('3.2 MB → 280 KB')
  })
})
