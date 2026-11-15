<template>
  <span class="status-dot" :class="`status-dot--${tone}`" aria-hidden="true"></span>
</template>

<script setup lang="ts">
/**
 * One status dot (styles in styles/status.css). `off` is the shared hollow ring for "not taking part": a stopped
 * account in a group, a paused account, a disabled user, the paused live indicator. Size follows --dot-size (7px).
 */
export type StatusDotTone = 'ok' | 'back' | 'exempt' | 'observe' | 'off' | 'error' | 'info'

defineProps<{ tone: StatusDotTone }>()
</script>
