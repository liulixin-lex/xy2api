<template>
  <button
    type="button"
    class="live-status"
    :class="{ 'live-status--stale': status === 'stale' }"
    :aria-label="t(`common.liveStatus.${status}Label`)"
    v-tip="tip"
    data-test="live-status"
    :data-status="status"
    @click="toggle"
  >
    <StatusDot :tone="TONES[status]" />
    <span>{{ t(`common.liveStatus.${status}`) }}</span>
  </button>
</template>

<script setup lang="ts">
/**
 * The live indicator for a table on the live engine (spec 4.2): live, paused (the shared hollow ring), out of date.
 * Clicking it pauses live updates, or resumes them when paused.
 *
 *   <LiveStatusIndicator :status="table.status.value" :pause="table.pause" :resume="table.resume" :detail="…" />
 *
 * `detail` adds one line to the live tip (for example which data refreshes how often).
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { LiveStatus } from '@/composables/useLiveTable'
import { vTip } from '@/utils/tip'
import StatusDot, { type StatusDotTone } from './StatusDot.vue'

const props = defineProps<{
  status: LiveStatus
  pause: () => void
  resume: () => void
  detail?: string
}>()

const { t } = useI18n()

const TONES: Record<LiveStatus, StatusDotTone> = { live: 'ok', paused: 'off', stale: 'back' }

const tip = computed(() => {
  const head = t(`common.liveStatus.${props.status}Tip`)
  const action = props.status === 'paused' ? t('common.liveStatus.clickToResume') : t('common.liveStatus.clickToPause')
  return [props.status === 'live' && props.detail ? `${head} · ${props.detail}` : head, action].join('\n')
})

const toggle = () => {
  if (props.status === 'paused') props.resume()
  else props.pause()
}
</script>

<style scoped>
@reference "../../style.css";

.live-status {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  height: var(--control-height);
  padding: 0 8px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  white-space: nowrap;
  transition: background-color var(--motion-fast) var(--ease-out), color var(--motion-fast) var(--ease-out);
}
@media (hover: hover) {
  .live-status:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.live-status:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.live-status--stale,
.live-status--stale:hover {
  color: var(--color-status-back-text);
}
/* Touch: 40px high to hit, its look unchanged (7n-3). */
@media (pointer: coarse) {
  .live-status {
    position: relative;
  }
  .live-status::after {
    position: absolute;
    inset: -4px 0;
    content: '';
  }
}
</style>
