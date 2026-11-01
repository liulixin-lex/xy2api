<template>
  <Transition
    :css="!reducedMotion"
    enter-active-class="transition-[opacity,transform] duration-200 ease-out"
    leave-active-class="transition-[opacity,transform] duration-150 ease-in"
    enter-from-class="opacity-0 translate-y-2 scale-[0.98]"
    leave-to-class="opacity-0 translate-y-2 scale-[0.98]"
  >
    <section
      v-if="open"
      ref="root"
      class="fixed z-[45] flex flex-col overflow-hidden bg-white shadow-2xl ring-1 ring-gray-200 focus:outline-none dark:bg-dark-900 dark:ring-dark-700"
      :class="fullscreen ? 'inset-0' : 'rounded-2xl'"
      :style="panelStyle"
      role="dialog"
      :aria-label="t('supportTickets.title')"
      tabindex="-1"
      data-testid="support-panel"
      @keydown.esc="onEscape"
    >
      <header class="flex items-center gap-2 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
        <Icon name="chat" size="md" class="text-primary-600 dark:text-primary-400" />
        <h2 class="min-w-0 flex-1 truncate text-sm font-semibold text-gray-900 dark:text-white">{{ t('supportTickets.title') }}</h2>
        <router-link
          v-if="authenticated"
          to="/support-tickets?tab=chat"
          class="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-xs text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-white"
          data-testid="support-panel-full"
          @click="emit('close')"
        >
          <Icon name="externalLink" size="xs" />
          {{ t('supportChat.widget.openFull') }}
        </router-link>
        <button
          type="button"
          class="rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
          :aria-label="t('common.close')"
          data-testid="support-panel-close"
          @click="emit('close')"
        >
          <Icon name="x" size="md" />
        </button>
      </header>

      <template v-if="authenticated">
        <div class="border-b border-gray-100 px-4 py-2 dark:border-dark-700">
          <SupportTabs :model-value="tab" class="w-full" :tabs="tabs" :label="t('supportTickets.title')" @update:model-value="setTab" />
        </div>
        <SupportChatConversation v-if="tab === 'chat'" :active="open && tab === 'chat'" />
        <SupportWidgetTickets v-else :active="open && tab === 'tickets'" @navigate="emit('close')" />
      </template>

      <div v-else class="flex-1 space-y-4 overflow-y-auto p-5" data-testid="support-guest">
        <p class="whitespace-pre-wrap rounded-2xl bg-primary-50 px-4 py-3 text-sm leading-relaxed text-primary-900 dark:bg-primary-900/30 dark:text-primary-100">{{ t('supportChat.guest.welcome') }}</p>
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('supportChat.guest.loginHint') }}</p>
        <div v-if="contactInfo" class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-800">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('supportChat.guest.contact') }}</p>
          <p class="mt-1 whitespace-pre-wrap break-words text-sm text-gray-800 dark:text-gray-100" data-testid="support-guest-contact">{{ contactInfo }}</p>
        </div>
        <div class="flex gap-2">
          <router-link :to="loginTarget" class="btn btn-primary flex-1" data-testid="support-guest-login" @click="rememberReopen">
            {{ t('supportChat.guest.login') }}
          </router-link>
          <router-link to="/register" class="btn btn-secondary flex-1" data-testid="support-guest-register" @click="emit('close')">
            {{ t('supportChat.guest.register') }}
          </router-link>
        </div>
      </div>
    </section>
  </Transition>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import SupportChatConversation from '@/features/support-chat/SupportChatConversation.vue'
import SupportTabs from '@/features/support-chat/SupportTabs.vue'
import { useMotionPreference } from '@/composables/useMotionPreference'
import { useAppStore, useAuthStore, useSupportChatStore, useSupportTicketStore } from '@/stores'
import type { BubblePoint, BubbleSide } from './snapBubble'
import { SUPPORT_WIDGET_REOPEN_KEY } from './supportWidgetVisibility'
import SupportWidgetTickets from './SupportWidgetTickets.vue'

const PANEL_WIDTH = 380
const PANEL_HEIGHT = 600
const PANEL_GAP = 12
const EDGE_MARGIN = 16
const SMALL_SCREEN_PX = 640

type PanelTab = 'chat' | 'tickets'

const props = defineProps<{
  open: boolean
  anchor: BubblePoint
  anchorSize: number
  side: BubbleSide
  viewport: { width: number; height: number }
}>()
const emit = defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()
const route = useRoute()
const auth = useAuthStore()
const app = useAppStore()
const chatStore = useSupportChatStore()
const ticketStore = useSupportTicketStore()
const { reducedMotion } = useMotionPreference()

const root = ref<HTMLElement | null>(null)
const tab = ref<PanelTab>('chat')
const authenticated = computed(() => auth.isAuthenticated)
const contactInfo = computed(() => app.contactInfo.trim())
const fullscreen = computed(() => props.viewport.width < SMALL_SCREEN_PX)
const loginTarget = computed(() => `/login?redirect=${encodeURIComponent(route.fullPath)}`)
const tabs = computed<Array<{ value: PanelTab; label: string; badge?: number }>>(() => [
  { value: 'chat', label: t('supportChat.tabs.messages'), badge: chatStore.userChatUnread },
  { value: 'tickets', label: t('supportChat.tabs.tickets'), badge: ticketStore.userUnread },
])

/** Desktop card: opens toward the bubble's side, above it when the bubble sits in the lower half, kept on screen. */
const panelStyle = computed(() => {
  if (fullscreen.value) return undefined
  const { width: viewportWidth, height: viewportHeight } = props.viewport
  const width = Math.min(PANEL_WIDTH, viewportWidth - EDGE_MARGIN * 2)
  const height = Math.min(PANEL_HEIGHT, viewportHeight - EDGE_MARGIN * 2)
  const size = props.anchorSize
  const rawLeft = props.side === 'right' ? props.anchor.x + size - width : props.anchor.x
  const lowerHalf = props.anchor.y + size / 2 > viewportHeight / 2
  const rawTop = lowerHalf ? props.anchor.y - PANEL_GAP - height : props.anchor.y + size + PANEL_GAP
  const left = Math.min(Math.max(EDGE_MARGIN, rawLeft), viewportWidth - width - EDGE_MARGIN)
  const top = Math.min(Math.max(EDGE_MARGIN, rawTop), viewportHeight - height - EDGE_MARGIN)
  return {
    width: `${width}px`,
    height: `${height}px`,
    left: `${left}px`,
    top: `${top}px`,
    transformOrigin: `${props.side === 'right' ? 'right' : 'left'} ${lowerHalf ? 'bottom' : 'top'}`,
  }
})

function setTab(value: string) {
  tab.value = value === 'tickets' ? 'tickets' : 'chat'
}

/** Esc closes the panel unless an IME is composing, something inside handled it or a dialog is open on top. */
function onEscape(event: KeyboardEvent) {
  // Esc that cancels an IME candidate (macOS Chrome: isComposing; Safari: keyCode 229 only) keeps the draft.
  if (event.isComposing || event.keyCode === 229) return
  if (event.defaultPrevented || document.body.classList.contains('modal-open')) return
  event.preventDefault()
  emit('close')
}

function rememberReopen() {
  try {
    sessionStorage.setItem(SUPPORT_WIDGET_REOPEN_KEY, '1')
  } catch {
    // Without storage the panel simply stays closed after login.
  }
  emit('close')
}

watch(() => props.open, async (open) => {
  if (!open) return
  await nextTick()
  root.value?.focus({ preventScroll: true })
}, { immediate: true })
</script>
