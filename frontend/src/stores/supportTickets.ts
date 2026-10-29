/**
 * Sidebar badge counts for support tickets: tickets with unread admin replies
 * (users) and tickets waiting for an admin (admins). Refreshed on login, on
 * navigation at most once a minute, and right after ticket actions — no timer.
 */
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getAdminSummary, getMySummary } from '@/api/supportTickets'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'
import { useAppStore } from './app'
import { useAuthStore } from './auth'
import { useAdminAccessStore } from './adminAccess'

const THROTTLE_MS = 60_000

export const useSupportTicketStore = defineStore('supportTickets', () => {
  const userUnread = ref(0)
  const adminPending = ref(0)
  let lastFetch = 0
  let generation = 0

  async function refresh(force = false) {
    const authStore = useAuthStore()
    const appStore = useAppStore()
    const access = useAdminAccessStore()
    if (!authStore.isAuthenticated || !resolveFeatureFlag(appStore.cachedPublicSettings, FeatureFlags.supportTickets)) {
      reset()
      return
    }
    const accessGeneration = generation
    if (authStore.canAccessAdmin && !authStore.isAdmin) {
      await access.ensureLoaded()
      if (accessGeneration !== generation) return
      if (!access.can('support_tickets:view')) { reset(); return }
    }
    const now = Date.now()
    if (!force && lastFetch > 0 && now - lastFetch < THROTTLE_MS) return
    lastFetch = now
    const current = ++generation
    const identity = `${authStore.user?.id}:${authStore.user?.role}`
    const stillCurrent = () => current === generation && authStore.isAuthenticated &&
      identity === `${authStore.user?.id}:${authStore.user?.role}` &&
      resolveFeatureFlag(appStore.cachedPublicSettings, FeatureFlags.supportTickets) &&
      (!authStore.canAccessAdmin || authStore.isAdmin || access.can('support_tickets:view'))
    try {
      if (authStore.isAdmin || authStore.canAccessAdmin) {
        const summary = await getAdminSummary()
        if (stillCurrent()) adminPending.value = summary.pending_count
      } else {
        const summary = await getMySummary()
        if (stillCurrent()) userUnread.value = summary.unread_count
      }
    } catch {
      // Let the next navigation retry.
      if (current === generation) lastFetch = 0
    }
  }

  function setUserUnread(count: number) {
    userUnread.value = Math.max(0, count)
  }

  function setAdminPending(count: number) {
    adminPending.value = Math.max(0, count)
  }

  function reset() {
    generation++
    lastFetch = 0
    userUnread.value = 0
    adminPending.value = 0
  }

  return { userUnread, adminPending, refresh, setUserUnread, setAdminPending, reset }
})
