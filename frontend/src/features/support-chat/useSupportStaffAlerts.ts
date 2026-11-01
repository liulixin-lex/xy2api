import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getAdminChatSummary } from '@/api/supportChat'
import { useAppStore, useAuthStore, useSupportChatStore, useSupportTicketStore } from '@/stores'
import { useAdminAccessStore } from '@/stores/adminAccess'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'

export const STAFF_SUMMARY_VISIBLE_MS = 15_000
export const STAFF_SUMMARY_HIDDEN_MS = 60_000
export const STAFF_SUMMARY_INBOX_MS = 4_000
export const SUPPORT_ALERT_SOUND_KEY = 'support_alert_sound'
export const SUPPORT_ALERT_NOTIFY_KEY = 'support_alert_notify'
export const SUPPORT_ALERT_LAST_ID_PREFIX = 'support_alert_last_id:'
const NOTIFICATION_TAG = 'xy2-support-chat'
export const SUPPORT_ALERT_LOCK_PREFIX = 'support_alert:'
const CHAT_INBOX_PATH = '/admin/support-tickets?tab=chats'

function readFlag(key: string): boolean {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    localStorage.setItem(key, value ? '1' : '0')
  } catch {
    // Only lasts for this page.
  }
}

// Shared by the App-level poller and the toggles in the 客户服务 page header.
const soundEnabled = ref(readFlag(SUPPORT_ALERT_SOUND_KEY))
const notifyEnabled = ref(readFlag(SUPPORT_ALERT_NOTIFY_KEY))
const soundSuspended = ref(false)
const inboxWatchers = ref(0)
let audioContext: AudioContext | null = null

type AudioContextConstructor = typeof AudioContext

function audioConstructor(): AudioContextConstructor | null {
  if (typeof window === 'undefined') return null
  const scope = window as unknown as { AudioContext?: AudioContextConstructor; webkitAudioContext?: AudioContextConstructor }
  return scope.AudioContext ?? scope.webkitAudioContext ?? null
}

function syncSuspended() {
  soundSuspended.value = soundEnabled.value && (!audioContext || audioContext.state !== 'running')
}

/** Creates or resumes the AudioContext; browsers only allow this inside a user gesture. */
function unlockAudio() {
  const Ctor = audioConstructor()
  if (!Ctor) return
  try {
    audioContext ??= new Ctor()
    if (audioContext.state === 'suspended') {
      void audioContext.resume().then(syncSuspended, syncSuspended)
    }
  } catch {
    audioContext = null
  }
  syncSuspended()
}

/** Two short sine tones synthesized with WebAudio, so no audio file ships. */
function beep() {
  if (!audioContext || audioContext.state !== 'running') {
    syncSuspended()
    return
  }
  const start = audioContext.currentTime
  for (const [offset, frequency] of [[0, 880], [0.18, 1175]] as const) {
    const oscillator = audioContext.createOscillator()
    const gain = audioContext.createGain()
    oscillator.type = 'sine'
    oscillator.frequency.value = frequency
    gain.gain.setValueAtTime(0.0001, start + offset)
    gain.gain.exponentialRampToValueAtTime(0.2, start + offset + 0.02)
    gain.gain.exponentialRampToValueAtTime(0.0001, start + offset + 0.15)
    oscillator.connect(gain).connect(audioContext.destination)
    oscillator.start(start + offset)
    oscillator.stop(start + offset + 0.16)
  }
}

export function notificationsSupported(): boolean {
  return typeof window !== 'undefined' && 'Notification' in window
}

/** The two-state 提示音 / 桌面通知 buttons. */
export function useSupportAlertToggles() {
  const { t } = useI18n()
  const appStore = useAppStore()

  function toggleSound() {
    const next = !soundEnabled.value
    soundEnabled.value = next
    writeFlag(SUPPORT_ALERT_SOUND_KEY, next)
    if (next) unlockAudio()
    syncSuspended()
  }

  async function toggleNotify() {
    if (notifyEnabled.value) {
      notifyEnabled.value = false
      writeFlag(SUPPORT_ALERT_NOTIFY_KEY, false)
      return
    }
    if (!notificationsSupported()) return
    let permission = Notification.permission
    if (permission === 'default') {
      try {
        permission = await Notification.requestPermission()
      } catch {
        permission = 'denied'
      }
    }
    const granted = permission === 'granted'
    notifyEnabled.value = granted
    writeFlag(SUPPORT_ALERT_NOTIFY_KEY, granted)
    if (!granted) appStore.showWarning(t('supportChat.alerts.notifyDenied'))
  }

  return {
    soundEnabled,
    notifyEnabled,
    soundSuspended,
    notifySupported: notificationsSupported(),
    toggleSound,
    toggleNotify,
    unlockAudio,
  }
}

/** While the inbox is on screen the summary is polled every 4s; call the returned function to stop. */
export function watchStaffSummaryFast(): () => void {
  inboxWatchers.value++
  let released = false
  return () => {
    if (released) return
    released = true
    inboxWatchers.value = Math.max(0, inboxWatchers.value - 1)
  }
}

/**
 * Staff-side alerts, mounted once in App.vue. Polls ONLY the unaudited chat
 * summary (15s visible, 60s hidden, 4s while the inbox is open), keeps the
 * sidebar/title counts current, and alerts when latest_user_message_id grows.
 * The first poll is the baseline; a per-user localStorage "last alerted id"
 * (synced by the storage event) keeps several tabs from alerting twice. When
 * Web Locks exist, the read-compare-write of that id runs under a per-user lock
 * so tabs that poll at the same moment claim each new message only once.
 */
export function useSupportStaffAlerts() {
  const { t } = useI18n()
  const router = useRouter()
  const auth = useAuthStore()
  const app = useAppStore()
  const access = useAdminAccessStore()
  const chatStore = useSupportChatStore()
  const ticketStore = useSupportTicketStore()

  const enabled = computed(() =>
    auth.isAuthenticated &&
    auth.canAccessAdmin &&
    access.can('support_tickets:view') &&
    resolveFeatureFlag(app.cachedPublicSettings, FeatureFlags.supportChat)
  )
  const lastAlertedKey = computed(() => `${SUPPORT_ALERT_LAST_ID_PREFIX}${auth.user?.id ?? 0}`)
  let baseline: number | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let generation = 0

  function storedLastAlerted(): number {
    try {
      return Number(localStorage.getItem(lastAlertedKey.value)) || 0
    } catch {
      return 0
    }
  }

  function notify() {
    if (!notifyEnabled.value || !notificationsSupported() || Notification.permission !== 'granted') return
    try {
      // Same tag keeps one entry; renotify makes Chromium show the banner and play
      // the OS sound again instead of replacing the old notification silently.
      // lib.dom has no renotify field yet, hence the widened type.
      const options: NotificationOptions & { renotify?: boolean } = {
        body: t('supportChat.alerts.notificationBody'),
        tag: NOTIFICATION_TAG,
        renotify: true,
      }
      const notification = new Notification(t('supportChat.alerts.notificationTitle'), options)
      notification.onclick = () => {
        window.focus()
        void router.push(CHAT_INBOX_PATH)
        notification.close()
      }
    } catch {
      // Some mobile browsers expose Notification but refuse the constructor.
    }
  }

  /** Read-compare-write of the last alerted id; true when this tab owns the alert for `latest`. */
  function claimAlert(latest: number): boolean {
    const stored = storedLastAlerted()
    if (baseline === null) {
      baseline = Math.max(latest, stored)
      return false
    }
    if (latest <= baseline || latest <= stored) {
      baseline = Math.max(baseline, latest, stored)
      return false
    }
    baseline = latest
    try {
      localStorage.setItem(lastAlertedKey.value, String(latest))
    } catch {
      // Without storage each tab alerts on its own.
    }
    return true
  }

  /** Claims under a per-user Web Lock when available, so tabs in other processes cannot claim the same id. */
  async function checkAlert(latest: number, version: number) {
    const locks = typeof navigator !== 'undefined' ? navigator.locks : undefined
    const stillCurrent = () => version === generation && enabled.value
    let claimed = false
    if (locks && typeof locks.request === 'function') {
      try {
        // restart() may reset the baseline while this waits for the lock.
        claimed = await locks.request(`${SUPPORT_ALERT_LOCK_PREFIX}${auth.user?.id ?? 0}`, () => stillCurrent() && claimAlert(latest))
      } catch {
        claimed = stillCurrent() && claimAlert(latest)
      }
    } else {
      claimed = claimAlert(latest)
    }
    if (!claimed || !stillCurrent()) return
    if (soundEnabled.value) beep()
    notify()
  }

  function stop() {
    if (timer) clearTimeout(timer)
    timer = undefined
  }

  function schedule() {
    stop()
    if (!enabled.value) return
    const hidden = document.visibilityState === 'hidden'
    const delay = hidden ? STAFF_SUMMARY_HIDDEN_MS : inboxWatchers.value > 0 ? STAFF_SUMMARY_INBOX_MS : STAFF_SUMMARY_VISIBLE_MS
    timer = setTimeout(() => { void poll() }, delay)
  }

  async function poll() {
    stop()
    if (!enabled.value) return
    const version = generation
    try {
      const summary = await getAdminChatSummary()
      if (version !== generation || !enabled.value) return
      chatStore.setAdminSummary(summary)
      ticketStore.setAdminPending(summary.ticket_pending)
      await checkAlert(summary.latest_user_message_id, version)
    } catch {
      // Try again on the next tick.
    }
    if (version === generation) schedule()
  }

  function restart() {
    generation++
    baseline = null
    stop()
    if (enabled.value) void poll()
  }

  function onVisibility() {
    if (document.visibilityState === 'visible') void poll()
    else schedule()
  }

  function onStorage(event: StorageEvent) {
    if (event.key === lastAlertedKey.value && event.newValue) {
      baseline = Math.max(baseline ?? 0, Number(event.newValue) || 0)
    }
  }

  function onFirstGesture() {
    if (soundEnabled.value) unlockAudio()
    if (!soundSuspended.value) {
      document.removeEventListener('pointerdown', onFirstGesture, true)
      document.removeEventListener('keydown', onFirstGesture, true)
    }
  }

  // Operators answer only after /me/access says they may.
  watch(() => [auth.user?.id, auth.canAccessAdmin, auth.isAdmin] as const, ([id, canAccessAdmin, isAdmin]) => {
    if (id && canAccessAdmin && !isAdmin) void access.ensureLoaded()
  }, { immediate: true })
  watch([enabled, () => auth.user?.id], restart, { immediate: true })
  watch(inboxWatchers, (count, old) => {
    if (count > old) void poll()
    else schedule()
  })

  syncSuspended()
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('storage', onStorage)
  document.addEventListener('pointerdown', onFirstGesture, true)
  document.addEventListener('keydown', onFirstGesture, true)

  onBeforeUnmount(() => {
    generation++
    stop()
    document.removeEventListener('visibilitychange', onVisibility)
    window.removeEventListener('storage', onStorage)
    document.removeEventListener('pointerdown', onFirstGesture, true)
    document.removeEventListener('keydown', onFirstGesture, true)
  })

  return { enabled }
}
