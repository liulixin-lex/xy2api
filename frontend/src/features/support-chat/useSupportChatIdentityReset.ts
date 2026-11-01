import { watch } from 'vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useSupportChatStore } from '@/stores/supportChat'

/** The chat store belongs to one user, one role and one state of the chat switch. */
export function supportChatIdentityKey(id: number | undefined, role: string | undefined, chatEnabled: boolean | undefined): string {
  return `${id ?? ''}|${role ?? ''}|${chatEnabled ?? ''}`
}

/**
 * Mounted once in App.vue: drops the chat store (and its image URLs) when the
 * user, role or chat switch really changes. The watch source is a primitive key,
 * so the 60s refreshUser() or a settings refetch that replaces the objects with
 * the same values does not reset anything.
 */
export function useSupportChatIdentityReset() {
  const auth = useAuthStore()
  const app = useAppStore()
  const chat = useSupportChatStore()
  watch(
    () => supportChatIdentityKey(auth.user?.id, auth.user?.role, app.cachedPublicSettings?.support_chat_enabled),
    () => chat.reset()
  )
}
