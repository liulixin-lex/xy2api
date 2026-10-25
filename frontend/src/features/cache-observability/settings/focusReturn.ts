import { watch } from 'vue'
import type { PoliciesState } from './usePolicies'

type FocusTarget = HTMLElement | null | undefined

/**
 * BaseDialog hands focus back to whatever was focused when it opened, but by the time the confirmation closes that
 * control may be disabled (the master switch while previewing, a card's preview button once its draft is saved), and
 * focus fell to <body> (m-new-1). Once the DOM has settled, give it to the first target that takes it.
 */
export function returnFocusAfterPreview(state: PoliciesState, key: string, targets: () => FocusTarget[]) {
  watch(
    () => state.preview.value?.key === key,
    (open, wasOpen) => {
      if (open || !wasOpen) return
      for (const target of targets()) {
        target?.focus()
        if (target && document.activeElement === target) return
      }
    },
    { flush: 'post' },
  )
}
