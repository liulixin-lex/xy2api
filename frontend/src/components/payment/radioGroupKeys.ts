const STEP: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }

/**
 * Keyboard for a button-based radio group (WAI-ARIA radio group): the arrow keys move to the previous / next enabled
 * option and pick it, wrapping at the ends; Home and End jump to the first and last. Bind on the role="radiogroup"
 * element; the options are its role="radio" buttons, of which only the picked one (or the first) sits in the Tab order.
 */
export function onRadioGroupKeydown(event: KeyboardEvent): void {
  const group = event.currentTarget as HTMLElement | null
  if (!group) return
  const options = Array.from(group.querySelectorAll<HTMLButtonElement>('[role="radio"]:not(:disabled)'))
  if (options.length === 0) return
  const current = options.indexOf(document.activeElement as HTMLButtonElement)
  let next: number
  if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = options.length - 1
  else if (event.key in STEP) next = current < 0 ? 0 : (current + STEP[event.key] + options.length) % options.length
  else return
  event.preventDefault()
  options[next].focus()
  options[next].click()
}
