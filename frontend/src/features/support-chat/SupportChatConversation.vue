<template>
  <div class="flex min-h-0 flex-1 flex-col" data-testid="chat-conversation">
    <SupportChatThread
      :thread="thread"
      viewer="user"
      image-scope="user"
      :welcome="store.welcome"
      can-recall
      :recall-expired-ids="recallExpired"
      @recall="onRecall"
    />
    <p
      v-if="thread.state.blocked"
      class="border-t border-amber-100 bg-amber-50 px-4 py-2 text-xs text-amber-800 dark:border-amber-900/40 dark:bg-amber-900/30 dark:text-amber-200"
      data-testid="chat-blocked"
    >{{ t('supportChat.blocked') }}</p>
    <SupportChatComposer v-else :placeholder="t('supportChat.placeholder')" @send="onSend" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { recallMyChatMessage, type SupportChatMessage } from '@/api/supportChat'
import { useAppStore } from '@/stores/app'
import { useSupportChatStore } from '@/stores/supportChat'
import { useChatThreadPolling } from './chatThread'
import type { CompressedImage } from './compressImage'
import SupportChatComposer from './SupportChatComposer.vue'
import SupportChatThread from './SupportChatThread.vue'
import { supportChatErrorMessage } from './supportChat'

// active: the conversation is on screen; it polls and reports reads only then.
const props = defineProps<{ active: boolean }>()
const { t } = useI18n()
const store = useSupportChatStore()
const appStore = useAppStore()
const thread = store.userThread()
const recallExpired = reactive(new Set<number>())

useChatThreadPolling(thread, computed(() => props.active))

function onSend(payload: { body: string; image: CompressedImage | null }) {
  void thread.send(payload.body, payload.image)
}

async function onRecall(message: SupportChatMessage) {
  try {
    const result = await recallMyChatMessage(message.id)
    thread.replace(result.message)
  } catch (err) {
    if ((err as { reason?: string } | null)?.reason === 'SUPPORT_CHAT_RECALL_EXPIRED') recallExpired.add(message.id)
    appStore.showError(supportChatErrorMessage(err, t))
  }
}

onMounted(() => {
  // Welcome text and the ticket badge come from the summary.
  if (!store.welcome) void store.refreshSummary()
})
</script>
