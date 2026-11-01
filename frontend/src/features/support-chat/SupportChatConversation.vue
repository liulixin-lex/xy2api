<template>
  <div class="flex min-h-0 flex-1 flex-col" data-testid="chat-conversation">
    <SupportChatThread
      :thread="thread"
      viewer="user"
      image-scope="user"
      :welcome="store.welcome"
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
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSupportChatStore } from '@/stores/supportChat'
import { useChatThreadPolling } from './chatThread'
import type { CompressedImage } from './compressImage'
import SupportChatComposer from './SupportChatComposer.vue'
import SupportChatThread from './SupportChatThread.vue'

// active: the conversation is on screen; it polls and reports reads only then.
const props = defineProps<{ active: boolean }>()
const { t } = useI18n()
const store = useSupportChatStore()
const thread = store.userThread()

useChatThreadPolling(thread, computed(() => props.active))

function onSend(payload: { body: string; image: CompressedImage | null }) {
  void thread.send(payload.body, payload.image)
}

onMounted(() => {
  // Welcome text and the ticket badge come from the summary.
  if (!store.welcome) void store.refreshSummary()
})
</script>
