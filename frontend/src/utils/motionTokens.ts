import { gsap } from 'gsap'
import { CustomEase } from 'gsap/CustomEase'

gsap.registerPlugin(CustomEase)

// Script animations read the timing tokens of styles/motion.css. The fallbacks repeat those values for
// documents without the stylesheet (unit tests).
const FALLBACKS: Record<string, string> = {
  '--motion-fast': '140ms',
  '--motion-base': '220ms',
  '--motion-slow': '420ms',
  '--motion-exit': '160ms',
  '--ease-out': 'cubic-bezier(0.16, 1, 0.3, 1)',
  '--ease-out-soft': 'cubic-bezier(0.22, 1, 0.36, 1)',
  '--ease-in': 'cubic-bezier(0.4, 0, 1, 1)',
  '--ease-emphasized': 'cubic-bezier(0.22, 1.06, 0.36, 1)'
}

function token(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || FALLBACKS[name]
}

/** Duration token in seconds, e.g. motionDuration('--motion-slow') = 0.42. */
export function motionDuration(name: string): number {
  return parseFloat(token(name)) / 1000
}

const eases = new Map<string, gsap.EaseFunction>()

/** Easing token (a CSS cubic-bezier) as a GSAP ease. */
export function motionEase(name: string): gsap.EaseFunction {
  let ease = eases.get(name)
  if (!ease) {
    const points = token(name).replace(/^cubic-bezier\(|\)$/g, '').replace(/\s+/g, '')
    ease = CustomEase.create(`motion${name}`, points)
    eases.set(name, ease)
  }
  return ease
}
