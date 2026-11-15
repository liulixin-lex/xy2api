/**
 * Whether the primary pointer is coarse (a touch screen): true unless the device has a fine pointer such as a mouse.
 * Interaction follows the pointer, layout follows the width (spec 4.3): one media query shared by every caller.
 */
import { computed } from 'vue'
import { createSharedComposable, useMediaQuery } from '@vueuse/core'

export const usePointerCoarse = createSharedComposable(() => {
  const fine = useMediaQuery('(pointer: fine)')
  return computed(() => !fine.value)
})
