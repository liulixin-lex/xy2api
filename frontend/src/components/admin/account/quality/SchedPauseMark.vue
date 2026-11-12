<template>
  <span
    v-if="label"
    v-tip="label"
    role="img"
    tabindex="0"
    :aria-label="label"
    class="sq-icon-mark sq-focus inline-flex h-[14px] w-[14px] flex-shrink-0 items-center justify-center normal-case"
    data-sq-pause
  >
    <svg viewBox="0 0 14 14" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
      <circle cx="7" cy="7" r="6.25" />
      <path d="M5.5 4.75v4.5M8.5 4.75v4.5" stroke-linecap="round" />
    </svg>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { vTip } from '@/utils/tip'

// Weak mark shown where quality scheduling is off or its view is stale (design spec 0.3); nothing else changes.
const props = defineProps<{ globalEnabled?: boolean | null; stale?: boolean | null }>()
const { t } = useI18n()
const label = computed(() => {
  if (props.globalEnabled === false) return t('admin.schedQuality.tip.globalOff')
  if (props.stale) return t('admin.schedQuality.tip.stale')
  return ''
})
</script>
