<template>
  <Teleport to="body">
    <Transition :css="false" appear @enter="onEnter" @leave="onLeave">
      <div v-if="show" class="fixed inset-0" :style="{ zIndex: stackZ }" data-base-drawer @keydown.tab="trapFocus">
        <div data-drawer-overlay class="absolute inset-0 bg-black/55" aria-hidden="true" @click="closeOnOverlay && emit('close')" />
        <section
          ref="panelRef"
          data-drawer-panel
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
          tabindex="-1"
          class="absolute inset-y-0 right-0 flex w-full flex-col border-l border-gray-200 bg-white shadow-2xl focus:outline-none dark:border-dark-700 dark:bg-dark-800 sm:w-[clamp(640px,50vw,760px)]"
        >
          <header class="modal-header gap-3">
            <div class="min-w-0 flex-1">
              <slot name="header" :title-id="titleId">
                <h2 :id="titleId" class="modal-title truncate">{{ title }}</h2>
              </slot>
            </div>
            <button
              type="button"
              class="-mr-2 rounded-xl p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-600 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-200"
              :aria-label="t('common.close')"
              @click="emit('close')"
            >
              <Icon name="x" size="md" />
            </button>
          </header>
          <div ref="bodyRef" class="modal-body overscroll-contain">
            <slot />
          </div>
          <footer v-if="$slots.footer" class="modal-footer">
            <slot name="footer" />
          </footer>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import Icon from '@/components/icons/Icon.vue'
import { isReducedMotion } from '@/composables/useMotionPreference'
import { motionDuration, motionEase } from '@/utils/motionTokens'
import { closeOverlay, isTopOverlay, openOverlay } from '@/utils/overlayStack'

// Right-side drawer shell: slots only. Focus handling follows BaseDialog; its layer order (z-index, focus, Escape)
// is the opening order shared with dialogs, so whichever opened last is on top.
const props = withDefaults(defineProps<{ show: boolean; title?: string; closeOnOverlay?: boolean }>(), {
  title: '',
  closeOnOverlay: true
})
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const titleId = `drawer-title-${useId()}`
const token = Symbol('drawer')
const panelRef = ref<HTMLElement | null>(null)
const bodyRef = ref<HTMLElement | null>(null)
let trigger: HTMLElement | null = null
const stackZ = ref<number>()

const focusableElements = () => Array.from(panelRef.value?.querySelectorAll<HTMLElement>(
  'button, [href], input, select, textarea, summary, [tabindex]'
) ?? []).filter(element => !element.matches(':disabled, [tabindex="-1"]') &&
  !element.closest('[hidden], [inert], [aria-hidden="true"]') &&
  (element.checkVisibility?.({ checkVisibilityCSS: true }) ?? true))

function trapFocus(event: KeyboardEvent) {
  const elements = focusableElements()
  const first = elements[0], last = elements[elements.length - 1]
  if (!first) { event.preventDefault(); panelRef.value?.focus(); return }
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

// Focus goes back to the trigger only when the top layer closes; a layer still open above keeps it.
function release() {
  const wasTop = isTopOverlay(token)
  closeOverlay(token)
  if (wasTop && trigger?.isConnected) trigger.focus()
  trigger = null
}

watch(
  () => props.show,
  async (open) => {
    if (!open) return release()
    trigger = document.activeElement as HTMLElement | null
    stackZ.value = openOverlay(token)
    await nextTick()
    if (!props.show) return
    if (bodyRef.value) bodyRef.value.scrollTop = 0
    ;(focusableElements()[0] ?? panelRef.value)?.focus()
  },
  { immediate: true }
)

function onEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented || !props.show || !isTopOverlay(token)) return
  event.preventDefault()
  emit('close')
}

// Enter: the panel slides in 32px from the right and firms up from 0.4 opacity while the overlay fades in;
// exit is shorter and eased in. Only transform and opacity move. Reduced motion shows and hides at once.
let motion: gsap.Context | undefined
let running: gsap.core.Timeline | undefined

function play(el: Element, done: () => void, entering: boolean) {
  // Only the latest transition is kept in the context, so unmounting reverts what is on screen.
  running?.kill()
  motion?.clear()
  if (isReducedMotion()) return done()
  const overlay = el.querySelector('[data-drawer-overlay]')
  const panel = el.querySelector('[data-drawer-panel]')
  motion ??= gsap.context(() => {})
  motion.add(() => {
    running = entering
      ? gsap.timeline({ onComplete: done })
          .fromTo(overlay, { opacity: 0 }, { opacity: 1, duration: motionDuration('--motion-base'), ease: motionEase('--ease-out-soft'), clearProps: 'opacity' }, 0)
          .fromTo(panel, { x: 32, opacity: 0.4 }, { x: 0, opacity: 1, duration: motionDuration('--motion-slow'), ease: motionEase('--ease-out'), clearProps: 'transform,opacity' }, 0)
      : gsap.timeline({ defaults: { duration: motionDuration('--motion-exit'), ease: motionEase('--ease-in') }, onComplete: done })
          .to(overlay, { opacity: 0 }, 0)
          .to(panel, { x: 24, opacity: 0 }, 0)
  })
}

const onEnter = (el: Element, done: () => void) => play(el, done, true)
const onLeave = (el: Element, done: () => void) => play(el, done, false)

onMounted(() => document.addEventListener('keydown', onEscape))
onUnmounted(() => {
  document.removeEventListener('keydown', onEscape)
  motion?.revert()
  release()
})
</script>
