import type { Directive } from 'vue'

/**
 * v-tip="text": short tip (design spec E): shown after 400ms hover or at once on keyboard focus, hidden on
 * leave, blur, scroll or Escape (Escape only closes the tip, not the layer under it). One shared element with
 * role="tooltip"; while it is shown the target's aria-describedby also points to it (the target's own description
 * stays). No native title. Empty text = no tip. Line breaks in the text are kept. v-tip.focus: keyboard focus only.
 */
const TIP_ID = 'sq-tip'
const HOVER_DELAY_MS = 400
const GAP = 6

type TipTarget = HTMLElement & {
  __tipText?: string
  __tipHandlers?: Record<string, (event: Event) => void>
}

let tipEl: HTMLDivElement | null = null
let current: TipTarget | null = null
let timer: ReturnType<typeof setTimeout> | undefined

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

const describedBy = (el: HTMLElement) => (el.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((id) => id && id !== TIP_ID)
const setDescribedBy = (el: HTMLElement, ids: string[]) => {
  if (ids.length) el.setAttribute('aria-describedby', ids.join(' '))
  else el.removeAttribute('aria-describedby')
}

function show(target: TipTarget) {
  clearTimeout(timer)
  const text = target.__tipText
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
  if (tipEl) tipEl.hidden = true
  if (current) setDescribedBy(current, describedBy(current))
  current = null
  document.removeEventListener('keydown', onKeydown, true)
  window.removeEventListener('scroll', hide, true)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !current) return
  event.preventDefault()
  event.stopPropagation()
  hide()
}

const keyboardFocus = (el: HTMLElement) => {
  try {
    return el.matches(':focus-visible')
  } catch {
    return true
  }
}

export const vTip: Directive<TipTarget, string | null | undefined> = {
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
      handlers.mouseleave = () => { if (current === el) hide(); else clearTimeout(timer) }
    }
    el.__tipHandlers = handlers
    for (const [name, handler] of Object.entries(el.__tipHandlers)) el.addEventListener(name, handler)
  },
  updated(el, binding) {
    el.__tipText = binding.value ?? ''
    if (current !== el) return
    if (!el.__tipText) hide()
    else if (tipEl) {
      tipEl.textContent = el.__tipText
      place(el, tipEl)
    }
  },
  beforeUnmount(el) {
    if (current === el) hide()
    for (const [name, handler] of Object.entries(el.__tipHandlers ?? {})) el.removeEventListener(name, handler)
    delete el.__tipHandlers
  }
}
