import { describe, expect, it } from 'vitest'
import {
  bubbleTopRange,
  clampBubblePoint,
  DEFAULT_BUBBLE_POSITION,
  parseBubblePosition,
  resolveBubblePoint,
  snapBubble,
  type BubbleBounds,
} from '../snapBubble'

const bounds = (extra: Partial<BubbleBounds> = {}): BubbleBounds => ({
  viewportWidth: 1280, viewportHeight: 800, size: 56, margin: 16, leftInset: 0, topInset: 64, bottomInset: 0, ...extra,
})

describe('snapBubble', () => {
  it('starts bottom-right', () => {
    expect(DEFAULT_BUBBLE_POSITION).toEqual({ side: 'right', topRatio: 1 })
    expect(resolveBubblePoint(DEFAULT_BUBBLE_POSITION, bounds())).toEqual({ x: 1280 - 16 - 56, y: 800 - 16 - 56 })
  })

  it('snaps to the nearer side and keeps the height', () => {
    expect(snapBubble({ x: 300, y: 400 }, bounds())).toEqual({ side: 'left', topRatio: (400 - 80) / (728 - 80) })
    expect(snapBubble({ x: 900, y: 80 }, bounds())).toEqual({ side: 'right', topRatio: 0 })
  })

  it('keeps the left edge right of the desktop sidebar', () => {
    const withSidebar = bounds({ leftInset: 256 })
    expect(resolveBubblePoint({ side: 'left', topRatio: 0 }, withSidebar)).toEqual({ x: 272, y: 80 })
    // The middle moves with the sidebar: x=600 is left of (272 + 1208 + 56) / 2.
    expect(snapBubble({ x: 600, y: 200 }, withSidebar).side).toBe('left')
    expect(clampBubblePoint({ x: 0, y: 0 }, withSidebar)).toEqual({ x: 272, y: 80 })
  })

  it('never goes under the header and safe areas', () => {
    const notch = bounds({ topInset: 64 + 44, bottomInset: 34 })
    expect(bubbleTopRange(notch)).toEqual({ min: 124, max: 800 - 34 - 16 - 56 })
    expect(clampBubblePoint({ x: 2000, y: -50 }, notch)).toEqual({ x: 1208, y: 124 })
    expect(snapBubble({ x: 10, y: 5000 }, notch)).toEqual({ side: 'left', topRatio: 1 })
  })

  it('re-clamps the same stored position for a new viewport', () => {
    const stored = { side: 'right' as const, topRatio: 0.5 }
    expect(resolveBubblePoint(stored, bounds())).toEqual({ x: 1208, y: 404 })
    expect(resolveBubblePoint(stored, bounds({ viewportWidth: 390, viewportHeight: 700, size: 52 }))).toEqual({ x: 322, y: 80 + (632 - 80) / 2 })
    // A viewport too short for any range pins the bubble to the top limit.
    expect(resolveBubblePoint(stored, bounds({ viewportHeight: 100 })).y).toBe(80)
    expect(snapBubble({ x: 0, y: 300 }, bounds({ viewportHeight: 100 })).topRatio).toBe(0)
  })

  it('parses only valid stored positions', () => {
    expect(parseBubblePosition(JSON.stringify({ side: 'left', topRatio: 0.25 }))).toEqual({ side: 'left', topRatio: 0.25 })
    expect(parseBubblePosition(JSON.stringify({ side: 'up', topRatio: 7 }))).toEqual({ side: 'right', topRatio: 1 })
    expect(parseBubblePosition('{broken')).toEqual(DEFAULT_BUBBLE_POSITION)
    expect(parseBubblePosition(null)).toEqual(DEFAULT_BUBBLE_POSITION)
  })
})
