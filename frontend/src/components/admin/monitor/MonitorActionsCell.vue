<template>
  <!-- Touch: 40px buttons 8px apart, the block's negative margins keeping the row's height. -->
  <!-- A phone: the buttons wrap within the screen width less the name and 24px of cell padding, so
       with the table scrolled all the way none sits under the frozen columns. -->
  <div class="flex items-center gap-1 [@media(pointer:coarse)]:-my-1.5 [@media(pointer:coarse)]:gap-2 max-md:flex-wrap max-md:max-w-[calc(100vw_-_26px_-_var(--table-identity-width))]">
    <button
      @click="$emit('run', row)"
      :disabled="running"
      class="flex flex-col items-center justify-center gap-0.5 rounded-lg p-1.5 [@media(pointer:coarse)]:min-h-10 [@media(pointer:coarse)]:min-w-10 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
    >
      <Icon name="refresh" size="sm" :class="running ? 'animate-spin' : ''" />
      <span class="text-xs">{{ t('admin.channelMonitor.runNow') }}</span>
    </button>
    <button
      data-testid="monitor-duplicate"
      :title="duplicateTitle"
      :disabled="duplicating || Boolean(row.api_key_decrypt_failed)"
      @click="$emit('duplicate', row)"
      class="flex flex-col items-center justify-center gap-0.5 rounded-lg p-1.5 [@media(pointer:coarse)]:min-h-10 [@media(pointer:coarse)]:min-w-10 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700 dark:hover:text-primary-400"
    >
      <Icon name="copy" size="sm" />
      <span class="text-xs">
        {{ duplicating ? t('admin.channelMonitor.duplicating') : t('admin.channelMonitor.duplicate') }}
      </span>
    </button>
    <button
      @click="$emit('edit', row)"
      class="flex flex-col items-center justify-center gap-0.5 rounded-lg p-1.5 [@media(pointer:coarse)]:min-h-10 [@media(pointer:coarse)]:min-w-10 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
    >
      <Icon name="edit" size="sm" />
      <span class="text-xs">{{ t('common.edit') }}</span>
    </button>
    <button
      @click="$emit('delete', row)"
      class="flex flex-col items-center justify-center gap-0.5 rounded-lg p-1.5 [@media(pointer:coarse)]:min-h-10 [@media(pointer:coarse)]:min-w-10 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
    >
      <Icon name="trash" size="sm" />
      <span class="text-xs">{{ t('common.delete') }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  row: ChannelMonitor
  running: boolean
  duplicating: boolean
}>()

defineEmits<{
  (e: 'run', row: ChannelMonitor): void
  (e: 'duplicate', row: ChannelMonitor): void
  (e: 'edit', row: ChannelMonitor): void
  (e: 'delete', row: ChannelMonitor): void
}>()

const { t } = useI18n()
const duplicateTitle = computed(() => {
  if (props.row.api_key_decrypt_failed) return t('admin.channelMonitor.duplicateKeyUnavailable')
  if (props.duplicating) return t('admin.channelMonitor.duplicating')
  return t('admin.channelMonitor.duplicate')
})
</script>
