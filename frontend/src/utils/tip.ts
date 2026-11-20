import type { Directive } from 'vue'
import { anyOverlayOpen } from './overlayStack'

/**
 * v-tip="text": short tip (design spec E): shown after 400ms hover, or on keyboard focus (an element not wholly in
 * view is scrolled in first, at once); hidden on leave, blur, scroll or Escape. With no layer open Escape only closes
 * the tip; while a popover, panel or dialog is open the key goes on to it as well, so one press closes both (7m-2).
 * One shared element with role="tooltip"; while it is shown the target's aria-describedby also points to it (the
 * target's own description stays). No native title. Empty text = no tip. Line breaks in the text are kept.
 * v-tip.focus: keyboard focus only. The value may be a function: it is called when the tip shows, so a countdown in it
 * needs no re-render to stay right. Instead of text the value may be a card (12.7): a title, a state at the top right,
 * column heads and rows of data, built node by node (text only, never markup) in the same element, with the same
 * timing and placement.
 */
const TIP_ID = 'sq-tip'
const HOVER_DELAY_MS = 400
const GAP = 6
const EDGE = 8

export type TipTone = 'ok' | 'back' | 'exempt' | 'observe' | 'stopped' | 'error' | 'info' | 'muted'
export interface TipCell { text: string; tone?: TipTone; dot?: boolean }
export interface TipCard {
  /** Top left. */
  title?: string
  /** Top right: the state, with its dot. */
  chip?: TipCell
  /** Column heads. */
  head?: string[]
  /** Rows of data, each with the same number of columns. */
  rows?: (string | TipCell)[][]
  /**
   * With three columns or more: the note column wraps in what is left, so the label and value columns keep their
   * width on a narrow screen (by default the note keeps its width and they give way with an ellipsis).
   */
  wrapEnd?: boolean
}
export type TipContent = string | TipCard

type TipText = TipContent | null | undefined | (() => TipContent | null | undefined)

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

/**
 * Above the target; below when it does not fit above; against the viewport's edge when it fits neither. Its size is
 * capped first, to the viewport less EDGE on each side (styles/schedQuality.css caps it further, 320px or a card's
 * 340px, and cuts what does not fit inside it), so it is placed at the size it shows at and no edge leaves the
 * screen (12.12-2).
 */
function place(target: HTMLElement, el: HTMLElement) {
  el.style.setProperty('--tip-room-w', `${window.innerWidth - EDGE * 2}px`)
  el.style.setProperty('--tip-room-h', `${window.innerHeight - EDGE * 2}px`)
  // Measured where nothing squeezes it (a fixed box at its last left has only the room right of it): the same size and
  // line breaks however the tip sat before.
  el.style.left = '0px'
  el.style.top = '0px'
  const rect = target.getBoundingClientRect()
  const width = el.offsetWidth
  const height = el.offsetHeight
  const left = Math.max(EDGE, Math.min(rect.left + rect.width / 2 - width / 2, window.innerWidth - width - EDGE))
  const above = rect.top - height - GAP
  const below = rect.bottom + GAP
  const top = above >= EDGE ? above : below + height <= window.innerHeight - EDGE ? below : Math.max(EDGE, window.innerHeight - height - EDGE)
  el.style.left = `${left}px`
  el.style.top = `${top}px`
}

const tipText = (target: TipTarget): TipContent => {
  const value = target.__tipText
  return (typeof value === 'function' ? value() : value) ?? ''
}

function node(tag: string, className: string, text?: string) {
  const el = document.createElement(tag)
  el.className = className
  if (text !== undefined) el.textContent = text
  return el
}

/** A cell's text with its tone and, for a state, its dot (the table's dot: a ring for stopped). */
function cell(className: string, value: string | TipCell) {
  const data = typeof value === 'string' ? { text: value } : value
  const el = node('span', data.tone ? `${className} sq-tip__tone--${data.tone}` : className)
  if (data.dot) el.appendChild(node('span', 'sq-tip__dot'))
  el.appendChild(document.createTextNode(data.text))
  return el
}

function render(el: HTMLElement, content: TipContent) {
  if (typeof content === 'string') {
    el.className = 'sq-tip'
    el.textContent = content
    return
  }
  el.className = 'sq-tip sq-tip--card'
  const parts: HTMLElement[] = []
  if (content.title || content.chip) {
    const top = node('div', 'sq-tip__top')
    if (content.title) top.appendChild(node('span', 'sq-tip__title', content.title))
    if (content.chip) top.appendChild(cell('sq-tip__chip', content.chip))
    parts.push(top)
  }
  const rows = content.rows ?? []
  const cols = content.head?.length ?? rows[0]?.length ?? 0
  if (cols) {
    // The first column is the label; with three columns or more the last one is a note, at the right.
    const column = (base: string, index: number) =>
      index === 0 ? `${base} sq-tip__first` : cols >= 3 && index === cols - 1 ? `${base} sq-tip__end` : base
    const wrapEnd = !!content.wrapEnd && cols >= 3
    const grid = node('div', wrapEnd ? 'sq-tip__grid sq-tip__grid--wrap' : 'sq-tip__grid')
    // Columns as wide as their text, a long one cut with an ellipsis when the card is at its widest; the note column
    // keeps its text and takes what is left over, so it sits against the right edge. With wrapEnd the note column
    // gives way instead, on two lines if it must.
    grid.style.gridTemplateColumns = wrapEnd
      ? `repeat(${cols - 1}, max-content) minmax(0, 1fr)`
      : cols >= 3 ? `repeat(${cols - 1}, minmax(0, max-content)) minmax(max-content, 1fr)` : `repeat(${cols}, minmax(0, max-content))`
    content.head?.forEach((text, index) => grid.appendChild(node('span', column('sq-tip__h', index), text)))
    for (const row of rows) row.forEach((value, index) => grid.appendChild(cell(column('sq-tip__c', index), value)))
    parts.push(grid)
  }
  el.replaceChildren(...parts)
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
  render(el, text)
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

/** Wholly inside the viewport and the visible box of its nearest scrolling ancestor. */
function wholeInView(el: HTMLElement) {
  const rect = el.getBoundingClientRect()
  let box = { top: 0, left: 0, bottom: window.innerHeight, right: window.innerWidth }
  for (let node = el.parentElement; node && node !== document.body && node !== document.documentElement; node = node.parentElement) {
    const { overflowX, overflowY } = getComputedStyle(node)
    if (!/auto|scroll|overlay/.test(`${overflowX} ${overflowY}`)) continue
    const r = node.getBoundingClientRect()
    box = { top: Math.max(box.top, r.top), left: Math.max(box.left, r.left), bottom: Math.min(box.bottom, r.bottom), right: Math.min(box.right, r.right) }
    break
  }
  return rect.top >= box.top && rect.left >= box.left && rect.bottom <= box.bottom && rect.right <= box.right
}

/**
 * Scrolls an element into view at once: smooth scrolling (html has scroll-behavior: smooth) is off for the call, which
 * also stops a smooth scroll the browser started for the focus. Not behavior: 'instant', which older Safari ignores.
 */
function jumpIntoView(el: HTMLElement) {
  const root = document.documentElement
  const before = root.style.scrollBehavior
  root.style.scrollBehavior = 'auto'
  try {
    el.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  } finally {
    root.style.scrollBehavior = before
  }
}

/** A tip waiting to show on keyboard focus (showOnFocus). */
let pending: { target: TipTarget; cancel: () => void } | null = null

/**
 * Keyboard focus on an element not wholly in view would have the browser scroll it in, smoothly, and a scroll hides
 * the tip. So the element is scrolled in here at once, and the tip shows two frames later, once that jump's scroll
 * events have gone by, if the element still has keyboard focus; a scroll after that hides it as any other. Escape
 * (passed on to an open layer, as for a shown tip), blur or unmount before then cancel it.
 */
function showOnFocus(target: TipTarget) {
  pending?.cancel()
  if (!wholeInView(target)) jumpIntoView(target)
  let frame = 0
  const onKey = (event: KeyboardEvent) => {
    if (event.key !== 'Escape') return
    if (!anyOverlayOpen()) {
      event.preventDefault()
      event.stopPropagation()
    }
    state.cancel()
  }
  const state = {
    target,
    cancel: () => {
      cancelAnimationFrame(frame)
      document.removeEventListener('keydown', onKey, true)
      if (pending === state) pending = null
    }
  }
  frame = requestAnimationFrame(() => {
    frame = requestAnimationFrame(() => {
      state.cancel()
      if (document.activeElement === target && keyboardFocus(target)) show(target)
    })
  })
  document.addEventListener('keydown', onKey, true)
  pending = state
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
      focus: () => { if (keyboardFocus(el)) showOnFocus(el) },
      blur: () => {
        if (pending?.target === el) pending.cancel()
        if (current === el) hide()
      }
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
      render(tipEl, text)
      place(el, tipEl)
    }
  },
  beforeUnmount(el) {
    if (pending?.target === el) pending.cancel()
    if (current === el) hide()
    for (const [name, handler] of Object.entries(el.__tipHandlers ?? {})) el.removeEventListener(name, handler)
    delete el.__tipHandlers
  }
}
