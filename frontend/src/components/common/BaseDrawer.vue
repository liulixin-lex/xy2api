<template>
  <Teleport to="body">
    <Transition :css="false" appear @enter="onEnter" @leave="onLeave">
      <div v-if="show" ref="rootRef" class="fixed inset-0" :style="{ zIndex: stackZ }" data-base-drawer @keydown.tab="trapTab($event, panelRef)">
        <div data-drawer-overlay class="absolute inset-0 bg-black/55" aria-hidden="true" @click="closeOnOverlay && emit('close')" />
        <section
          ref="panelRef"
          data-drawer-panel
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
          tabindex="-1"
          class="absolute inset-y-0 right-0 flex w-full flex-col bg-white shadow-2xl focus:outline-none dark:bg-dark-900 md:w-[min(720px,100vw)] md:border-l md:border-gray-200 md:dark:border-dark-700"
        >
          <header class="relative flex flex-shrink-0 flex-col gap-3 border-b border-gray-200 px-6 pb-4 pt-5 dark:border-dark-700 md:flex-row md:items-start md:gap-4">
            <div class="min-w-0 flex-1 max-md:pr-10">
              <slot name="header" :title-id="titleId">
                <h2 :id="titleId" class="truncate text-lg font-semibold text-gray-900 dark:text-white">{{ title }}</h2>
              </slot>
            </div>
            <!-- 关闭图标在 Tab 顺序里排第一（设计 B.13），视觉上在最右 -->
            <div class="flex flex-wrap items-center gap-2 md:flex-nowrap">
              <button
                type="button"
                class="-mr-2 rounded-xl p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-600 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-200 md:order-last max-md:absolute max-md:right-4 max-md:top-4"
                :aria-label="closeLabel || t('common.close')"
                @click="emit('close')"
              >
                <Icon name="x" size="md" />
              </button>
              <slot name="header-actions" />
            </div>
          </header>
          <div ref="bodyRef" class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-6">
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
import { closeOverlay, holdFocus, isTopOverlay, openOverlay, syncOverlays, trapTab } from '@/utils/overlayStack'

// Right-side drawer shell: slots only. Layer order, inert, focus and Escape follow utils/overlayStack (spec 0.2).
const props = withDefaults(defineProps<{
  show: boolean
  title?: string
  closeLabel?: string
  closeOnOverlay?: boolean
  /** Changing it while open brings the drawer back to the top, with the focused element as its new opener. */
  raiseKey?: number
}>(), {
  title: '',
  closeLabel: '',
  closeOnOverlay: true,
  raiseKey: 0
})
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const titleId = `drawer-title-${useId()}`
const token = Symbol('drawer')
const rootRef = ref<HTMLElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const bodyRef = ref<HTMLElement | null>(null)
const stackZ = ref<number>()

async function open(scrollTop: boolean) {
  stackZ.value = openOverlay(token, { root: () => rootRef.value, panel: () => panelRef.value })
  await nextTick()
  if (!props.show) return
  syncOverlays()
  if (scrollTop && bodyRef.value) bodyRef.value.scrollTop = 0
  panelRef.value?.focus()
}

watch(
  () => props.show,
  (isOpen) => {
    if (isOpen) void open(true)
    else closeOverlay(token)
  },
  { immediate: true }
)

watch(() => props.raiseKey, () => {
  if (props.show) void open(false)
})

function onEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented || !props.show || !isTopOverlay(token)) return
  event.preventDefault()
  emit('close')
}

// Spec F.3: the panel comes in from 24px right (320ms, emphasized ease) while firming up from 0.4 opacity, the overlay
// fades in; exit is shorter and eased in. Only transform and opacity move. Under reduced motion (system setting or
// the user's menu switch) the drawer is shown and hidden at once.
const mm = gsap.matchMedia()
let motion: gsap.Context | undefined
let running: gsap.core.Timeline | undefined

onMounted(() => {
  document.addEventListener('keydown', onEscape)
  mm.add('(prefers-reduced-motion: no-preference)', (context) => {
    motion = context
    return () => { motion = undefined }
  })
})

function play(el: Element, done: () => void, entering: boolean) {
  running?.kill()
  motion?.clear()
  const overlay = el.querySelector<HTMLElement>('[data-drawer-overlay]')
  const panel = el.querySelector<HTMLElement>('[data-drawer-panel]')
  if (!motion || isReducedMotion() || !overlay || !panel) {
    gsap.set([overlay, panel], { clearProps: 'transform,opacity' })
    return done()
  }
  const finish = () => {
    panel.style.willChange = ''
    done()
  }
  panel.style.willChange = 'transform'
  motion.add(() => {
    running = entering
      ? gsap.timeline({ onComplete: finish })
          .fromTo(overlay, { opacity: 0 }, { opacity: 1, duration: motionDuration('--motion-base'), ease: motionEase('--ease-out-soft'), clearProps: 'opacity' }, 0)
          .fromTo(panel, { x: 24 }, { x: 0, duration: 0.32, ease: motionEase('--ease-emphasized'), clearProps: 'transform' }, 0)
          .fromTo(panel, { opacity: 0.4 }, { opacity: 1, duration: motionDuration('--motion-base'), ease: motionEase('--ease-out-soft'), clearProps: 'opacity' }, 0)
      : gsap.timeline({ defaults: { duration: motionDuration('--motion-exit'), ease: motionEase('--ease-in') }, onComplete: finish })
          .to(overlay, { opacity: 0 }, 0)
          .to(panel, { x: 16, opacity: 0 }, 0)
  })
}

const onEnter = (el: Element, done: () => void) => play(el, done, true)
const onLeave = (el: Element, done: () => void) => play(el, done, false)

onUnmounted(() => {
  document.removeEventListener('keydown', onEscape)
  mm.revert()
  closeOverlay(token)
})

defineExpose({ body: bodyRef, holdFocus: () => holdFocus(token) })
</script>
