<template>
  <form
    class="border-t border-gray-100 bg-white p-3 dark:border-dark-700 dark:bg-dark-900"
    :class="dragging ? 'ring-2 ring-inset ring-primary-400' : ''"
    data-testid="chat-composer"
    @submit.prevent="submit"
    @dragover.prevent="onDragOver"
    @dragleave="dragging = false"
    @drop.prevent="onDrop"
  >
    <div v-if="image || compressing" class="mb-2 flex items-center gap-2 rounded-xl bg-gray-50 p-2 dark:bg-dark-800" data-testid="chat-image-chip">
      <img v-if="previewUrl" :src="previewUrl" alt="" class="h-10 w-10 shrink-0 rounded-lg object-cover" />
      <Icon v-else name="refresh" size="sm" class="animate-spin text-gray-400" />
      <span class="min-w-0 flex-1 truncate text-xs text-gray-600 dark:text-gray-300" data-testid="chat-image-size">
        {{ image ? `${formatImageBytes(image.originalSize)} → ${formatImageBytes(image.size)}` : t('supportChat.image.compressing') }}
      </span>
      <button
        v-if="image"
        type="button"
        class="rounded-lg p-1 text-gray-400 hover:bg-gray-200 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
        :aria-label="t('supportChat.image.remove')"
        data-testid="chat-image-remove"
        @click="clearImage"
      >
        <Icon name="x" size="sm" />
      </button>
    </div>
    <textarea
      ref="input"
      v-model="body"
      rows="2"
      class="input max-h-40 min-h-[2.75rem] resize-none text-sm"
      :placeholder="placeholder"
      :aria-label="placeholder"
      :disabled="disabled"
      data-testid="chat-input"
      @keydown.enter.ctrl.prevent="submit"
      @keydown.enter.meta.prevent="submit"
      @paste="onPaste"
    ></textarea>
    <div class="mt-2 flex items-center justify-between gap-2">
      <div class="flex min-w-0 items-center gap-2">
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="disabled || compressing"
          :title="t('supportChat.image.add')"
          :aria-label="t('supportChat.image.add')"
          data-testid="chat-image-pick"
          @click="fileInput?.click()"
        >
          <Icon name="upload" size="sm" />
        </button>
        <input ref="fileInput" type="file" accept="image/*" class="hidden" data-testid="chat-image-input" @change="onFileChange" />
        <span class="truncate text-[11px]" :class="tooLong ? 'font-medium text-red-600 dark:text-red-400' : 'text-gray-400'" data-testid="chat-counter">
          {{ tooLong ? t('supportTickets.form.counter', { count: length, max: SUPPORT_CHAT_BODY_MAX }) : t('supportChat.shortcut') }}
        </span>
      </div>
      <button type="submit" class="btn btn-primary btn-sm" :disabled="!canSend" data-testid="chat-send">
        {{ t('supportChat.send') }}
      </button>
    </div>
  </form>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { SUPPORT_CHAT_BODY_MAX } from '@/api/supportChat'
import { supportTicketLength } from '@/api/supportTickets'
import { useAppStore } from '@/stores/app'
import { compressImage, formatImageBytes, type CompressedImage } from './compressImage'
import { supportChatImageErrorMessage } from './supportChat'

const props = defineProps<{ placeholder: string; disabled?: boolean }>()
const emit = defineEmits<{ (e: 'send', payload: { body: string; image: CompressedImage | null }): void }>()
const { t } = useI18n()
const appStore = useAppStore()

const body = ref('')
const image = ref<CompressedImage | null>(null)
const previewUrl = ref('')
const compressing = ref(false)
const dragging = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const input = ref<HTMLTextAreaElement | null>(null)
let attempt = 0

const length = computed(() => supportTicketLength(body.value))
const tooLong = computed(() => length.value > SUPPORT_CHAT_BODY_MAX)
const canSend = computed(() => !props.disabled && !compressing.value && !tooLong.value && (body.value.trim() !== '' || !!image.value))

function setPreview(next: CompressedImage | null) {
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = next ? URL.createObjectURL(next.blob) : ''
  image.value = next
}

function clearImage() {
  attempt++
  compressing.value = false
  setPreview(null)
}

async function useFile(file: File | null | undefined) {
  if (!file || props.disabled) return
  const current = ++attempt
  setPreview(null)
  compressing.value = true
  try {
    const result = await compressImage(file)
    if (current === attempt) setPreview(result)
  } catch (err) {
    if (current === attempt) appStore.showError(supportChatImageErrorMessage(err, t))
  } finally {
    if (current === attempt) compressing.value = false
  }
}

function onFileChange(event: Event) {
  const target = event.target as HTMLInputElement
  void useFile(target.files?.[0])
  target.value = ''
}

function imageFrom(list: DataTransferItemList | FileList | null | undefined): File | null {
  if (!list) return null
  for (const entry of Array.from(list as ArrayLike<DataTransferItem | File>)) {
    const file = entry instanceof File ? entry : entry.kind === 'file' ? entry.getAsFile() : null
    if (file && file.type.startsWith('image/')) return file
  }
  return null
}

/**
 * A browser's "Copy image" (Firefox) also writes the image URL as text/plain and
 * an <img> as text/html. That text is only a label for the image, not content
 * the user wants to type.
 */
function isImageLabel(data: DataTransfer, text: string): boolean {
  if (!/^(https?|data|blob|file):\S+$/i.test(text)) return false
  if (data.types?.includes('text/rtf')) return false
  const html = data.getData('text/html')
  if (!html) return true
  return /<img\b/i.test(html) && !html.replace(/<!--[\s\S]*?-->/g, '').replace(/<[^>]*>/g, '').trim()
}

function onPaste(event: ClipboardEvent) {
  const data = event.clipboardData
  // Office apps (Excel, PowerPoint, OneNote) put a bitmap of the selection next
  // to the text: any text wins and pastes normally; only a text-less clipboard
  // (or one whose text is just the copied image's URL, or the copied file's own
  // name as Finder writes it) attaches its image.
  const text = data?.getData('text/plain').trim() ?? ''
  const file = imageFrom(data?.items) ?? imageFrom(data?.files)
  if (data && text && !isImageLabel(data, text) && text !== file?.name) return
  if (!file) return
  event.preventDefault()
  void useFile(file)
}

function onDragOver(event: DragEvent) {
  if (event.dataTransfer?.types.includes('Files')) dragging.value = true
}

function onDrop(event: DragEvent) {
  dragging.value = false
  void useFile(imageFrom(event.dataTransfer?.files))
}

function submit() {
  if (!canSend.value) return
  emit('send', { body: body.value.trim(), image: image.value })
  body.value = ''
  // The pending message owns the image now; only drop the chip's own preview.
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = ''
  image.value = null
  input.value?.focus()
}

onBeforeUnmount(() => {
  attempt++
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
})
</script>
