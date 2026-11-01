/**
 * When the floating support bubble is shown. Pure so the whole matrix is tested;
 * App.vue feeds it from the stores and the current route.
 */
export interface SupportWidgetVisibilityInput {
  /** Effective public flag: the ticket switch and the chat switch are both on. */
  chatEnabled: boolean
  /** Admins and operators answer chats; they never see the bubble. */
  canAccessAdmin: boolean
  backendMode: boolean
  /** appStore.mobileOpen: the mobile sidebar drawer is open. Only counts below lg. */
  mobileOpen: boolean
  /** Viewport is lg (1024px) or wider; there the drawer does not exist and a stale mobileOpen is ignored. */
  desktop: boolean
  /** route.meta.hideSupportWidget */
  hiddenByRoute: boolean
}

export function shouldShowSupportWidget(input: SupportWidgetVisibilityInput): boolean {
  const drawerOpen = input.mobileOpen && !input.desktop
  return input.chatEnabled && !input.canAccessAdmin && !input.backendMode && !drawerOpen && !input.hiddenByRoute
}

const HIDDEN_PATHS = ['/login', '/register', '/email-verify', '/forgot-password', '/reset-password', '/setup']
const HIDDEN_SECTIONS = ['/auth', '/payment', '/support-tickets', '/admin']
const VISIBLE_EXCEPTIONS = ['/payment/result']

/**
 * Route paths that set meta.hideSupportWidget: sign-in and recovery pages, OAuth
 * callbacks, payment pages except the result page, setup, the 客户服务 page itself
 * (it has the full chat) and the admin console.
 */
export function routeHidesSupportWidget(path: string): boolean {
  if (VISIBLE_EXCEPTIONS.includes(path)) return false
  if (HIDDEN_PATHS.includes(path)) return true
  return HIDDEN_SECTIONS.some((section) => path === section || path.startsWith(`${section}/`))
}

/** sessionStorage flag: a guest clicked 「登录」 in the panel, so reopen it after login. */
export const SUPPORT_WIDGET_REOPEN_KEY = 'support_widget_reopen'
