import type { Directive } from 'vue'
import { anyOverlayOpen } from './overlayStack'

/**
 * v-tip="text": short tip (design spec E): shown after 400ms hover or at once on keyboard focus, hidden on
 * leave, blur, scroll or Escape. With no layer open Escape only closes the tip; while a popover, panel or dialog is
 * open the key goes on to it as well, so one press closes both (7m-2). One shared element with
 * role="tooltip"; while it is shown the target's aria-describedby also points to it (the target's own description
 * stays). No native title. Empty text = no tip. Line breaks in the text are kept. v-tip.focus: keyboard focus only.
 * The value may be a function: it is called when the tip shows, so a countdown in it needs no re-render to stay right.
 */
const TIP_ID = 'sq-tip'
const HOVER_DELAY_MS = 400
const GAP = 6

type TipText = string | null | undefined | (() => string)

type TipTarget = HTMLElement & {
  __tipText?: TipText
  __tipHandlers?: Record<string, (event: Event) => void>
}

let tipEl: HTMLDivElement | null = null
let current: TipTarget | null = null
let timer: ReturnType<typeof setTimeout> | undefined
/**
 * A tip a tap or a pen brought up (showTipNow): the browser sends mouse events after the tap, and their mouseleave
 * would put the tip away at once. It stays until the next press elsewhere, a scroll, Escape or PINNED_MS (7q-6).
 */
const PINNED_MS = 4000
let pinned: TipTarget | null = null
let pinTimer: ReturnType<typeof setTimeout> | undefined
function onPressElsewhere(event: Event) {
  if (!(event.target instanceof Node && pinned?.contains(event.target))) hide()
}
/** The kind of the latest press: a mouse click's tip still goes on mouseleave, as a hover tip does. */
let pressType = ''
if (typeof window !== 'undefined') window.addEventListener('pointerdown', (event) => { pressType = event.pointerType }, true)

function tip() {
  if (!tipEl || !tipEl.isConnected) {
    tipEl = document.createElement('div')
    tipEl.id = TIP_ID
    tipEl.className = 'sq-tip'
    tipEl.setAttribute('role', 'tooltip')
    tipEl.hidden = true
    document.body.appendChild(tipEl)
  }
  return tipEl
}

function place(target: HTMLElement, el: HTMLElement) {
  const rect = target.getBoundingClientRect()
  const width = el.offsetWidth
  const height = el.offsetHeight
  const left = Math.max(8, Math.min(rect.left + rect.width / 2 - width / 2, window.innerWidth - width - 8))
  const above = rect.top - height - GAP
  el.style.left = `${left}px`
  el.style.top = `${above >= 8 ? above : rect.bottom + GAP}px`
}

const tipText = (target: TipTarget) => {
  const value = target.__tipText
  return (typeof value === 'function' ? value() : value) ?? ''
}

const describedBy = (el: HTMLElement) => (el.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((id) => id && id !== TIP_ID)
const setDescribedBy = (el: HTMLElement, ids: string[]) => {
  if (ids.length) el.setAttribute('aria-describedby', ids.join(' '))
  else el.removeAttribute('aria-describedby')
}

function show(target: TipTarget) {
  clearTimeout(timer)
  const text = tipText(target)
  if (!text || !target.isConnected) return
  if (current && current !== target) hide()
  const el = tip()
  el.textContent = text
  el.hidden = false
  place(target, el)
  current = target
  setDescribedBy(target, [...describedBy(target), TIP_ID])
  document.addEventListener('keydown', onKeydown, true)
  window.addEventListener('scroll', hide, true)
}

function hide() {
  clearTimeout(timer)
  clearTimeout(pinTimer)
  pinned = null
  document.removeEventListener('pointerdown', onPressElsewhere, true)
  if (tipEl) tipEl.hidden = true
  if (current) setDescribedBy(current, describedBy(current))
  current = null
  document.removeEventListener('keydown', onKeydown, true)
  window.removeEventListener('scroll', hide, true)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !current) return
  if (!anyOverlayOpen()) {
    event.preventDefault()
    event.stopPropagation()
  }
  hide()
}

const keyboardFocus = (el: HTMLElement) => {
  try {
    return el.matches(':focus-visible')
  } catch {
    return true
  }
}

/** Hides the tip that shows or is about to (a click that opens a layer from the element under it). */
export function hideTip() {
  hide()
}

/**
 * Shows an element's v-tip at once, without the hover delay: a click or tap on "+N" lists what it folds, a tap on a
 * pill tells why it has no dot. After a tap it stays through the mouse events that follow (pinned, above); after a
 * mouse click it goes on mouseleave, as a hover tip does.
 */
export function showTipNow(el: EventTarget | null) {
  if (!(el instanceof HTMLElement && (el as TipTarget).__tipHandlers)) return
  show(el as TipTarget)
  if (current !== el || pressType === 'mouse') return
  pinned = el as TipTarget
  clearTimeout(pinTimer)
  pinTimer = setTimeout(hide, PINNED_MS)
  document.addEventListener('pointerdown', onPressElsewhere, true)
}

export const vTip: Directive<TipTarget, TipText> = {
  mounted(el, binding) {
    el.__tipText = binding.value ?? ''
    const handlers: Record<string, (event: Event) => void> = {
      focus: () => { if (keyboardFocus(el)) show(el) },
      blur: () => { if (current === el) hide() }
    }
    if (!binding.modifiers.focus) {
      handlers.mouseenter = () => {
        clearTimeout(timer)
        timer = setTimeout(() => show(el), HOVER_DELAY_MS)
      }
      handlers.mouseleave = () => {
        if (pinned === el) return
        if (current === el) hide()
        else clearTimeout(timer)
      }
    }
    el.__tipHandlers = handlers
    for (const [name, handler] of Object.entries(el.__tipHandlers)) el.addEventListener(name, handler)
  },
  updated(el, binding) {
    el.__tipText = binding.value ?? ''
    if (current !== el) return
    const text = tipText(el)
    if (!text) hide()
    else if (tipEl) {
      tipEl.textContent = text
      place(el, tipEl)
    }
  },
  beforeUnmount(el) {
    if (current === el) hide()
    for (const [name, handler] of Object.entries(el.__tipHandlers ?? {})) el.removeEventListener(name, handler)
    delete el.__tipHandlers
  }
}
