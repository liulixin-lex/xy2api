import { onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'

/** Own request results for one mounted route/session, latest request wins. */
export function useSupportTicketRequests(identity: () => unknown, clear: () => void, allowed = () => true) {
  const auth = useAuthStore()
  const app = useAppStore()
  const permitted = () => allowed() && app.cachedPublicSettings?.support_ticket_enabled !== false
  const loading = ref(false)
  const busy = ref(false)
  let generation = 0
  let sequence = 0
  let mounted = true

  function reset() {
    generation++
    sequence++
    busy.value = false
    loading.value = false
    clear()
  }

  function begin(mutation = false) {
    if (!mounted || !permitted() || busy.value) return
    const version = generation
    const request = ++sequence
    const id = identity()
    if (mutation) {
      // Invalidate a poll which started before the mutation.
      loading.value = false
      busy.value = true
    } else loading.value = true
    const current = () => mounted && version === generation && request === sequence && identity() === id && permitted()
    return {
      current,
      finish() {
        if (!current()) return
        if (mutation) busy.value = false
        else loading.value = false
      }
    }
  }

  watch([identity, () => auth.user?.id, () => auth.user?.role, permitted], reset, { flush: 'sync' })
  onBeforeUnmount(() => { mounted = false; reset() })
  return { begin, reset, busy, loading }
}
