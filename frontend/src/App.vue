<script setup lang="ts">
import { RouterView, useRouter, useRoute } from 'vue-router'
import { computed, defineAsyncComponent, onMounted, onBeforeUnmount, watch } from 'vue'
import Toast from '@/components/common/Toast.vue'
import NavigationProgress from '@/components/common/NavigationProgress.vue'
import AdminComplianceDialog from '@/components/admin/AdminComplianceDialog.vue'
import { applyDocumentTitle, resolveRouteDocumentTitle, setDocumentTitleBadge } from '@/router/title'
import AnnouncementPopup from '@/components/common/AnnouncementPopup.vue'
import { useAppStore, useAuthStore, useSubscriptionStore, useAnnouncementStore, useAdminComplianceStore, useAdminSettingsStore, useSupportTicketStore, useSupportChatStore } from '@/stores'
import { getSetupStatus } from '@/api/setup'
import { updateFavicon } from '@/utils/branding'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'
import { useAdminAccessStore } from '@/stores/adminAccess'
import { resolveSiteBillingMode } from '@/utils/siteBillingMode'
import { shouldShowSupportWidget } from '@/features/support-widget/supportWidgetVisibility'
import { useSupportBadges } from '@/features/support-chat/useSupportBadges'
import { useSupportStaffAlerts } from '@/features/support-chat/useSupportStaffAlerts'
import { useSupportChatIdentityReset } from '@/features/support-chat/useSupportChatIdentityReset'
import { useMediaQuery } from '@vueuse/core'

// Floating support chat; its chunk only loads when the bubble is shown.
const SupportWidget = defineAsyncComponent(() => import('@/features/support-widget/SupportWidget.vue'))

const router = useRouter()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()
const subscriptionStore = useSubscriptionStore()
const announcementStore = useAnnouncementStore()
const adminComplianceStore = useAdminComplianceStore()
const supportTicketStore = useSupportTicketStore()
const supportChatStore = useSupportChatStore()
const ticketAdminAccess = useAdminAccessStore()
const adminSettingsStore = useAdminSettingsStore()
const { staffBadge } = useSupportBadges()
const supportStaffAlerts = useSupportStaffAlerts()

// lg+: the sidebar is fixed, so a mobileOpen left over from the drawer means nothing there.
const isDesktop = useMediaQuery('(min-width: 1024px)')
// Clear the drawer flag on reaching lg+, so going back below lg neither reopens
// the drawer nor unmounts the widget (and a draft in it).
watch(isDesktop, (desktop) => {
  if (desktop) appStore.setMobileOpen(false)
})
const supportWidgetVisible = computed(() => shouldShowSupportWidget({
  chatEnabled: isFeatureFlagEnabled(FeatureFlags.supportChat),
  canAccessAdmin: authStore.canAccessAdmin,
  backendMode: appStore.backendModeEnabled,
  mobileOpen: appStore.mobileOpen,
  desktop: isDesktop.value,
  hiddenByRoute: route.meta.hideSupportWidget === true,
}))

function updateDocumentTitle() {
  const customMenuItems = [
    ...(appStore.cachedPublicSettings?.custom_menu_items ?? []),
    ...(authStore.isAdmin ? adminSettingsStore.customMenuItems : []),
  ]
  applyDocumentTitle(resolveRouteDocumentTitle(route, appStore.siteName, customMenuItems, {
    billingMode: resolveSiteBillingMode(appStore.cachedPublicSettings),
  }))
}

// Staff see 「(N) 」 in the tab title: waiting chats plus pending tickets.
watch(
  () => (supportStaffAlerts.enabled.value ? staffBadge.value : 0),
  (count) => {
    setDocumentTitleBadge(count)
    updateDocumentTitle()
  }
)

// Watch for site settings changes and update favicon/title
watch(
  () => appStore.siteLogo,
  (newLogo) => {
    if (newLogo) {
      updateFavicon(newLogo)
    }
  },
  { immediate: true }
)

watch(
  [
    () => route.fullPath,
    () => route.meta.title,
    () => route.meta.titleKey,
    () => appStore.siteName,
    () => appStore.cachedPublicSettings?.custom_menu_items,
    () => appStore.cachedPublicSettings?.subscription_enabled,
    () => appStore.cachedPublicSettings?.payment_balance_disabled,
    () => authStore.isAdmin,
    () => adminSettingsStore.customMenuItems,
  ],
  updateDocumentTitle,
  { deep: true }
)

// Watch for authentication state and manage subscription data + announcements
function onVisibilityChange() {
  if (document.visibilityState === 'visible' && authStore.isAuthenticated) {
    announcementStore.fetchAnnouncements()
    supportTicketStore.refresh()
  }
}

// Public settings may arrive after login; fetch the ticket badge once they say the feature is on.
watch(
  () => appStore.cachedPublicSettings?.support_ticket_enabled,
  (enabled) => {
    if (enabled && authStore.isAuthenticated) supportTicketStore.refresh(true)
  }
)

function onAdminComplianceRequired(event: Event) {
  const detail = (event as CustomEvent<Record<string, string>>).detail || {}
  adminComplianceStore.requireAcknowledgement(detail)
}

// 订阅功能开关（opt-out）。关闭后不再预加载/轮询订阅接口；开关在登录后才到达时补启动，反向则清空。
const subscriptionFeatureEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.subscription))

function startSubscriptionSync() {
  subscriptionStore.fetchActiveSubscriptions().catch((error) => {
    console.error('Failed to preload subscriptions:', error)
  })
  subscriptionStore.startPolling()
}

watch(subscriptionFeatureEnabled, (enabled) => {
  if (!authStore.isAuthenticated) return
  if (enabled) {
    startSubscriptionSync()
  } else {
    subscriptionStore.clear()
  }
})

watch(
  () => authStore.isAuthenticated,
  (isAuthenticated, oldValue) => {
    if (isAuthenticated) {
      if (authStore.canAccessAdmin) {
        adminComplianceStore.fetchStatus().catch((error) => {
          console.error('Failed to fetch admin compliance status:', error)
        })
      }

      // User logged in: preload subscriptions and start polling (skipped when the
      // subscription feature is switched off; see the flag watcher below)
      if (subscriptionFeatureEnabled.value) {
        startSubscriptionSync()
      }

      // Announcements: new login vs page refresh restore
      if (oldValue === false) {
        // New login: delay 3s then force fetch
        setTimeout(() => announcementStore.fetchAnnouncements(true), 3000)
      } else {
        // Page refresh restore (oldValue was undefined)
        announcementStore.fetchAnnouncements()
      }

      // Support ticket badge (skips itself while the feature is off)
      supportTicketStore.refresh(true)

      // Register visibility change listener
      document.addEventListener('visibilitychange', onVisibilityChange)
    } else {
      // User logged out: clear data and stop polling
      subscriptionStore.clear()
      announcementStore.reset()
      adminComplianceStore.reset()
      supportTicketStore.reset()
      supportChatStore.reset()
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  },
  { immediate: true }
)

watch(
  () => [authStore.user?.id, authStore.user?.role, appStore.cachedPublicSettings?.support_ticket_enabled, ticketAdminAccess.version],
  () => {
    supportTicketStore.reset()
    void supportTicketStore.refresh(true)
  }
)

// The chat belongs to one signed-in user; drop it (and its image URLs) when the id, role or chat switch changes.
useSupportChatIdentityReset()

// Route change trigger (throttled by store)
router.afterEach(() => {
  if (authStore.isAuthenticated) {
    announcementStore.fetchAnnouncements()
    supportTicketStore.refresh()
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  window.removeEventListener('admin-compliance-required', onAdminComplianceRequired)
})

onMounted(async () => {
  window.addEventListener('admin-compliance-required', onAdminComplianceRequired)

  // Check if setup is needed
  try {
    const status = await getSetupStatus()
    if (status.needs_setup && route.path !== '/setup') {
      router.replace('/setup')
      return
    }
  } catch {
    // If setup endpoint fails, assume normal mode and continue
  }

  // Load public settings into appStore (will be cached for other components)
  await appStore.fetchPublicSettings()

  // Re-resolve document title now that site settings are available
  updateDocumentTitle()
})
</script>

<template>
  <NavigationProgress />
  <RouterView />
  <Toast />
  <AnnouncementPopup />
  <SupportWidget v-if="supportWidgetVisible" />
  <AdminComplianceDialog />
</template>
