<template>
  <Teleport to="body">
    <Transition :css="false" @enter="onEnter" @leave="onLeave">
      <div
        v-if="show"
        ref="rootRef"
        role="dialog"
        :aria-labelledby="titleId"
        class="fixed w-[320px] max-w-[calc(100vw-32px)] rounded-xl border border-gray-200 bg-white p-4 shadow-lg dark:border-dark-700 dark:bg-dark-800"
        :style="{ zIndex: stackZ, top: `${pos.top}px`, left: `${pos.left}px` }"
        data-sq-popover
        @keydown.tab="trapTab($event, rootRef)"
      >
        <h3 :id="titleId" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t(`admin.schedQuality.action.${mode}`) }}</h3>
        <div class="mt-3 grid grid-cols-[auto_1fr] items-center gap-x-3 gap-y-3 text-sm">
          <label :for="scopeId" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.override.scope') }}</label>
          <Select :id="scopeId" ref="scopeRef" v-model="groupId" :options="scopeOptions" :searchable="false" />
          <span class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.override.duration') }}</span>
          <div role="radiogroup" :aria-label="t('admin.schedQuality.override.duration')" class="inline-flex rounded-lg border border-gray-200 p-0.5 dark:border-dark-600">
            <button
              v-for="option in DURATIONS"
              :key="option"
              type="button"
              role="radio"
              :aria-checked="duration === option"
              :class="['sq-focus flex-1 rounded-md px-2.5 py-1 text-xs font-medium', duration === option ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400' : 'text-gray-600 dark:text-gray-300']"
              @click="duration = option"
            >{{ t(`admin.schedQuality.override.${option}`) }}</button>
          </div>
          <label :for="reasonId" class="text-gray-700 dark:text-gray-300">{{ t('admin.schedQuality.override.reason') }}</label>
          <input :id="reasonId" v-model="reason" type="text" class="input py-1.5" maxlength="200" />
        </div>
        <div class="mt-4 flex items-center justify-end gap-2">
          <span v-if="failed" class="sq-fail-word">{{ t('admin.schedQuality.result.failed') }}</span>
          <button type="button" class="btn btn-secondary btn-sm" @click="emit('close')">{{ t('admin.schedQuality.action.cancel') }}</button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="busy" :aria-busy="busy" @click="confirm">
            <span v-if="busy" class="spinner h-3.5 w-3.5" aria-hidden="true" />{{ t('admin.schedQuality.action.confirm') }}
          </button>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import { isReducedMotion } from '@/composables/useMotionPreference'
import { motionDuration, motionEase } from '@/utils/motionTokens'
import Select from '@/components/common/Select.vue'
import type { SchedOverrideMode, SchedOverrideRequest } from '@/types/schedQuality'
import { closeOverlay, isTopOverlay, openOverlay, trapTab } from '@/utils/overlayStack'

// Demote / exempt popover (design spec B.8): non-modal, anchored under its button, closed by Escape, an outside click
// or Cancel. Scope 0 = every group of the account.
type Duration = 'oneHour' | 'today' | 'forever'
const DURATIONS: Duration[] = ['oneHour', 'today', 'forever']

const props = defineProps<{
  show: boolean
  mode: SchedOverrideMode
  anchor: HTMLElement | null
  groups: Array<{ id: number; name: string }>
  defaultGroupId?: number
  /** Batch use defaults to one hour instead of always. */
  batch?: boolean
  busy?: boolean
  failed?: boolean
}>()
const emit = defineEmits<{ close: []; confirm: [request: SchedOverrideRequest] }>()
const { t } = useI18n()

const titleId = `sq-pop-${useId()}`
const scopeId = `sq-pop-scope-${useId()}`
const reasonId = `sq-pop-reason-${useId()}`
const token = Symbol('popover')
const rootRef = ref<HTMLElement | null>(null)
const scopeRef = ref<{ $el?: HTMLElement } | null>(null)
const stackZ = ref<number>()
const pos = reactive({ top: 0, left: 0 })
const groupId = ref(0)
const duration = ref<Duration>('forever')
const reason = ref('')

const scopeOptions = computed(() => [
  { value: 0, label: t('admin.schedQuality.override.allGroups') },
  ...props.groups.map((group) => ({ value: group.id, label: group.name }))
])

function expiresAt(): string | null {
  if (duration.value === 'forever') return null
  if (duration.value === 'oneHour') return new Date(Date.now() + 3600_000).toISOString()
  const end = new Date()
  end.setHours(23, 59, 59, 0)
  return end.toISOString()
}

const confirm = () => emit('confirm', { group_id: groupId.value, mode: props.mode, expires_at: expiresAt(), reason: reason.value.trim() })

function place() {
  const anchor = props.anchor?.getBoundingClientRect()
  const panel = rootRef.value
  if (!anchor || !panel) return
  const width = panel.offsetWidth
  const height = panel.offsetHeight
  const left = Math.max(16, Math.min(anchor.right - width, window.innerWidth - width - 16))
  const below = anchor.bottom + 6
  pos.left = left
  pos.top = below + height <= window.innerHeight - 8 ? below : Math.max(8, anchor.top - height - 6)
}

function onPointerDown(event: PointerEvent) {
  const target = event.target as Element | null
  if (!target || rootRef.value?.contains(target) || props.anchor?.contains(target) || target.closest('.select-dropdown-portal')) return
  emit('close')
}

function onEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented || !isTopOverlay(token)) return
  event.preventDefault()
  emit('close')
}

function detach() {
  document.removeEventListener('pointerdown', onPointerDown, true)
  document.removeEventListener('keydown', onEscape)
  window.removeEventListener('resize', place)
  closeOverlay(token)
}

watch(
  () => props.show,
  async (open) => {
    if (!open) return detach()
    groupId.value = props.defaultGroupId ?? 0
    duration.value = props.batch ? 'oneHour' : 'forever'
    reason.value = ''
    stackZ.value = openOverlay(token, { modal: false, root: () => rootRef.value, panel: () => rootRef.value })
    await nextTick()
    place()
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('keydown', onEscape)
    window.addEventListener('resize', place)
    scopeRef.value?.$el?.querySelector<HTMLElement>('button')?.focus()
  },
  { immediate: true }
)

// Spec F.3: fades in from 4px above (--motion-fast, --ease-out) and fades out in 160ms; at once under reduced motion.
let running: gsap.core.Tween | undefined
function onEnter(el: Element, done: () => void) {
  running?.kill()
  if (isReducedMotion()) return done()
  running = gsap.fromTo(el, { opacity: 0, y: -4 }, { opacity: 1, y: 0, duration: motionDuration('--motion-fast'), ease: motionEase('--ease-out'), clearProps: 'opacity,transform', onComplete: done })
}
function onLeave(el: Element, done: () => void) {
  running?.kill()
  if (isReducedMotion()) return done()
  running = gsap.to(el, { opacity: 0, duration: motionDuration('--motion-exit'), ease: motionEase('--ease-in'), onComplete: done })
}

onUnmounted(() => {
  running?.revert()
  detach()
})
</script>
