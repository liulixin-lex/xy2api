/**
 * Geometry of the floating support bubble. The stored position is relative
 * ({side, topRatio}) so it survives window resizes and device rotation.
 */
export type BubbleSide = 'left' | 'right'

export interface BubblePosition {
  side: BubbleSide
  /** 0 = highest allowed spot, 1 = lowest. */
  topRatio: number
}

export interface BubbleBounds {
  viewportWidth: number
  viewportHeight: number
  size: number
  /** Gap kept to the viewport edges. */
  margin: number
  /** Space taken on the left, e.g. the desktop sidebar. */
  leftInset: number
  /** Header height plus the top safe area. */
  topInset: number
  /** Bottom safe area. */
  bottomInset: number
}

export interface BubblePoint {
  x: number
  y: number
}

export const DEFAULT_BUBBLE_POSITION: BubblePosition = { side: 'right', topRatio: 1 }
export const BUBBLE_STORAGE_KEY = 'support_bubble_position'

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

export function bubbleTopRange(bounds: BubbleBounds): { min: number; max: number } {
  const min = bounds.topInset + bounds.margin
  const max = Math.max(min, bounds.viewportHeight - bounds.bottomInset - bounds.margin - bounds.size)
  return { min, max }
}

function bubbleLeftRange(bounds: BubbleBounds): { min: number; max: number } {
  const min = bounds.leftInset + bounds.margin
  const max = Math.max(min, bounds.viewportWidth - bounds.margin - bounds.size)
  return { min, max }
}

/** Screen point (top-left corner) for a stored position. */
export function resolveBubblePoint(position: BubblePosition, bounds: BubbleBounds): BubblePoint {
  const horizontal = bubbleLeftRange(bounds)
  const vertical = bubbleTopRange(bounds)
  return {
    x: position.side === 'left' ? horizontal.min : horizontal.max,
    y: vertical.min + clamp(position.topRatio, 0, 1) * (vertical.max - vertical.min),
  }
}

/** Keeps a point being dragged on screen. */
export function clampBubblePoint(point: BubblePoint, bounds: BubbleBounds): BubblePoint {
  const horizontal = bubbleLeftRange(bounds)
  const vertical = bubbleTopRange(bounds)
  return { x: clamp(point.x, horizontal.min, horizontal.max), y: clamp(point.y, vertical.min, vertical.max) }
}

/** Where a released bubble settles: the nearer side edge, at the same height (clamped). */
export function snapBubble(point: BubblePoint, bounds: BubbleBounds): BubblePosition {
  const horizontal = bubbleLeftRange(bounds)
  const vertical = bubbleTopRange(bounds)
  const center = point.x + bounds.size / 2
  const middle = (horizontal.min + horizontal.max + bounds.size) / 2
  const range = vertical.max - vertical.min
  return {
    side: center < middle ? 'left' : 'right',
    topRatio: range > 0 ? clamp((point.y - vertical.min) / range, 0, 1) : 0,
  }
}

export function parseBubblePosition(raw: string | null): BubblePosition {
  if (!raw) return { ...DEFAULT_BUBBLE_POSITION }
  try {
    const value = JSON.parse(raw) as Partial<BubblePosition>
    const side = value.side === 'left' || value.side === 'right' ? value.side : DEFAULT_BUBBLE_POSITION.side
    const topRatio = typeof value.topRatio === 'number' && Number.isFinite(value.topRatio) ? clamp(value.topRatio, 0, 1) : DEFAULT_BUBBLE_POSITION.topRatio
    return { side, topRatio }
  } catch {
    return { ...DEFAULT_BUBBLE_POSITION }
  }
}
