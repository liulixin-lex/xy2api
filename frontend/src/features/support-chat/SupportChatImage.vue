<template>
  <div class="max-w-full" :style="{ width: `${boxWidth}px` }" data-testid="chat-image">
    <div
      class="relative overflow-hidden rounded-xl bg-gray-100 ring-1 ring-gray-200 dark:bg-dark-700 dark:ring-dark-600"
      :style="{ aspectRatio: `${width} / ${height}` }"
    >
      <button
        v-if="url"
        type="button"
        class="block h-full w-full cursor-zoom-in focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
        :aria-label="t('supportChat.image.open')"
        data-testid="chat-image-open"
        @click="lightbox = true"
      >
        <img :src="url" :alt="t('supportChat.image.alt')" class="h-full w-full object-cover" draggable="false" />
      </button>
      <div v-else class="flex h-full w-full flex-col items-center justify-center gap-1 p-2 text-center text-xs text-gray-500 dark:text-gray-400">
        <template v-if="expired">
          <Icon name="clock" size="sm" />
          <span data-testid="chat-image-expired">{{ t('supportChat.image.expired') }}</span>
        </template>
        <span v-else-if="failure === 'gone'" data-testid="chat-image-unavailable">{{ t('supportChat.image.unavailable') }}</span>
        <template v-else-if="failure === 'retry'">
          <span>{{ t('supportChat.image.loadFailed') }}</span>
          <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" data-testid="chat-image-retry" @click="load">
            {{ t('supportChat.image.retry') }}
          </button>
        </template>
        <Icon v-else name="refresh" size="sm" class="animate-spin" />
      </div>
    </div>

    <BaseDialog :show="lightbox" :title="t('supportChat.image.preview')" width="full" :z-index="60" @close="lightbox = false">
      <img v-if="url" :src="url" :alt="t('supportChat.image.alt')" class="mx-auto max-h-[75vh] max-w-full object-contain" />
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { SupportChatImageRef } from '@/api/supportChat'
import { useSupportChatStore, type SupportChatImageScope } from '@/stores/supportChat'
import { supportChatErrorStatus } from './supportChat'

const MAX_BOX_WIDTH = 240

// localUrl: a pending upload that has no server id yet.
const props = defineProps<{ image: SupportChatImageRef | null; scope: SupportChatImageScope; localUrl?: string; localWidth?: number; localHeight?: number }>()
const { t } = useI18n()
const store = useSupportChatStore()

const fetchedUrl = ref('')
const failure = ref<'' | 'gone' | 'retry'>('')
const lightbox = ref(false)
let held: number | null = null

const width = computed(() => props.image?.width || props.localWidth || 1)
const height = computed(() => props.image?.height || props.localHeight || 1)
const boxWidth = computed(() => Math.min(MAX_BOX_WIDTH, Math.max(80, Math.round(MAX_BOX_WIDTH * Math.min(1, width.value / height.value)))))
const expired = computed(() => !props.localUrl && !!props.image?.purged)
const url = computed(() => props.localUrl || fetchedUrl.value)

function release() {
  if (held !== null) store.releaseImage(props.scope, held)
  held = null
  fetchedUrl.value = ''
}

async function load() {
  release()
  failure.value = ''
  const image = props.image
  if (props.localUrl || !image || image.purged) return
  held = image.id
  try {
    const objectUrl = await store.acquireImage(props.scope, image.id)
    if (held === image.id) fetchedUrl.value = objectUrl
  } catch (err) {
    if (held !== image.id) return
    held = null
    // Decided by the HTTP status only: 404/410 never come back, anything else may.
    const status = supportChatErrorStatus(err)
    failure.value = status === 404 || status === 410 ? 'gone' : 'retry'
  }
}

watch(() => [props.image?.id, props.image?.purged, props.localUrl], load, { immediate: true })
onBeforeUnmount(release)
</script>
