<template>
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1 bg-gray-50 px-4 py-2 text-[13px] dark:bg-dark-800/60" data-sq-recover-line>
    <span class="sq-num sq-text-3">{{ fmtAt(new Date(line.at).toISOString(), line.at) }}</span>
    <span v-if="!line.result" class="sq-fail-word">{{ t('admin.schedQuality.result.failed') }}</span>
    <template v-else>
      <span class="sq-text-3">{{ t('admin.schedQuality.recover.cleared') }}</span>
      <span class="sq-text" data-sq-cleared>{{ words(line.result.cleared) }}</span>
      <template v-if="line.result.kept.length">
        <span class="sq-text-3">{{ t('admin.schedQuality.recover.kept') }}</span>
        <span class="sq-text" data-sq-kept>{{ kept }}</span>
      </template>
      <template v-if="line.result.failed.length">
        <span class="sq-text-3">{{ t('admin.schedQuality.result.failed') }}</span>
        <span class="sq-fail-word" data-sq-failed>{{ words(line.result.failed.map((item) => item.kind)) }}</span>
      </template>
    </template>
    <button
      type="button"
      class="sq-focus ml-auto inline-flex h-6 w-6 items-center justify-center rounded text-gray-400 hover:bg-gray-200 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200"
      :aria-label="t('admin.schedQuality.action.close')"
      data-sq-recover-close
      @click="emit('close')"
    >×</button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RecoveryLine } from '@/composables/useAccountRecovery'
import { DASH, fmtAt, kindName } from '@/utils/schedQualityFormat'

// Result line of a recovery (FINAL §5): time, what was cleared, what is left (with its end), what failed.
const props = defineProps<{ line: RecoveryLine }>()
const emit = defineEmits<{ close: [] }>()
const { t, te } = useI18n()

const words = (kinds: string[]) => (kinds.length ? kinds.map((kind) => kindName(kind, t, te)).join(' ') : DASH)
const kept = computed(() =>
  (props.line.result?.kept ?? [])
    .map((item) => (item.until ? `${kindName(item.kind, t, te)}·${fmtAt(item.until, props.line.at)}` : kindName(item.kind, t, te)))
    .join(' ')
)
</script>
