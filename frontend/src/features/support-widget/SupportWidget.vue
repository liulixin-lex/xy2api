<template>
  <SupportBubble :bubble="bubble" :expanded="open" :badge="badge" @toggle="open = !open" />
  <SupportWidgetPanel
    :open="open"
    :anchor="bubble.point.value"
    :anchor-size="bubble.size.value"
    :side="bubble.position.value.side"
    :viewport="bubble.viewport.value"
    @close="open = false"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore, useAuthStore, useSupportChatStore } from '@/stores'
import { useSupportBadges } from '@/features/support-chat/useSupportBadges'
import SupportBubble from './SupportBubble.vue'
import SupportWidgetPanel from './SupportWidgetPanel.vue'
import { SUPPORT_WIDGET_REOPEN_KEY } from './supportWidgetVisibility'
import { useFloatingBubble } from './useFloatingBubble'

const WIDGET_SUMMARY_MS = 60_000
const WIDGET_SUMMARY_AFTER_SEND_MS = 15_000
const WIDGET_FAST_WINDOW_MS = 10 * 60_000
const DESKTOP_PX = 1024
const SIDEBAR_WIDTH_PX = 256
const SIDEBAR_COLLAPSED_PX = 72

// Mounted by App.vue only while supportWidgetVisibility says so. Guests see the
// bubble and a login prompt; they never call a /support-chat endpoint.
const route = useRoute()
const auth = useAuthStore()
const app = useAppStore()
const chatStore = useSupportChatStore()
const { userBadge } = useSupportBadges()

const open = ref(false)
const viewportWidth = ref(window.innerWidth)
// On desktop pages with the sidebar the left edge sits right of it.
const leftInset = computed(() => {
  if (viewportWidth.value < DESKTOP_PX || !auth.isAuthenticated || route.meta.requiresAuth === false) return 0
  return app.sidebarCollapsed ? SIDEBAR_COLLAPSED_PX : SIDEBAR_WIDTH_PX
})
const bubble = useFloatingBubble(leftInset)
const badge = computed(() => (auth.isAuthenticated ? userBadge.value : 0))
// While the panel is open the conversation polls messages itself.
const conversationVisible = computed(() => open.value)
let timer: ReturnType<typeof setTimeout> | undefined

function stopTimer() {
  if (timer) clearTimeout(timer)
  timer = undefined
}

/** Collapsed bubble: GET /support-chat every 60s, every 15s for 10 minutes after the user sent something. */
function scheduleSummary() {
  stopTimer()
  if (!auth.isAuthenticated || document.visibilityState === 'hidden') return
  const recentlySent = chatStore.lastSentAt > 0 && Date.now() - chatStore.lastSentAt < WIDGET_FAST_WINDOW_MS
  timer = setTimeout(async () => {
    timer = undefined
    if (!conversationVisible.value) await chatStore.refreshSummary()
    scheduleSummary()
  }, recentlySent ? WIDGET_SUMMARY_AFTER_SEND_MS : WIDGET_SUMMARY_MS)
}

function refreshNow() {
  if (!auth.isAuthenticated) return
  void chatStore.refreshSummary()
  scheduleSummary()
}

function onVisibility() {
  if (document.visibilityState === 'visible') refreshNow()
  else stopTimer()
}

function onResize() {
  viewportWidth.value = window.innerWidth
}

function reopenAfterLogin() {
  if (!auth.isAuthenticated) return
  try {
    if (sessionStorage.getItem(SUPPORT_WIDGET_REOPEN_KEY) !== '1') return
    sessionStorage.removeItem(SUPPORT_WIDGET_REOPEN_KEY)
    open.value = true
  } catch {
    // Storage unavailable: nothing to restore.
  }
}

watch(() => auth.user?.id, (id) => {
  if (!id) {
    open.value = false
    stopTimer()
    return
  }
  refreshNow()
  reopenAfterLogin()
}, { immediate: true })
watch(conversationVisible, (visible) => {
  if (!visible) refreshNow()
})

onMounted(() => {
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  stopTimer()
  document.removeEventListener('visibilitychange', onVisibility)
  window.removeEventListener('resize', onResize)
})
</script>
