<template>
  <button
    type="button"
    role="switch"
    class="account-switch"
    :aria-checked="checked ? 'true' : 'false'"
    :aria-label="label"
    :aria-busy="busy ? 'true' : undefined"
    :disabled="disabled"
    @click="emit('toggle', !checked)"
  >
    <span class="account-switch__thumb" />
  </button>
</template>

<script setup lang="ts">
/** The accounts table's 28×16 switch (scheduling, IQ checks): the brand teal for "on", a step deeper under the pointer (12.2). */
defineProps<{ checked: boolean; label: string; disabled?: boolean; busy?: boolean }>()
const emit = defineEmits<{ toggle: [next: boolean] }>()
</script>

<style scoped>
@reference "../../../../style.css";

.account-switch {
  position: relative;
  flex: none;
  width: var(--switch-width);
  height: var(--switch-height);
  padding: 0;
  border: 0;
  border-radius: var(--radius-full);
  background: var(--color-switch-off);
  cursor: pointer;
  transition: background-color var(--motion-fast) var(--ease-out);
}
.account-switch::after {
  /* a 44px-ish hit area without changing the layout */
  content: '';
  position: absolute;
  inset: -8px -6px;
}
/* Touch: 40 x 40 to hit (mobile list plan, item 17), its look unchanged. */
@media (pointer: coarse) {
  .account-switch::after {
    inset: -12px -6px;
  }
}
.account-switch__thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--color-switch-thumb-off);
  transition: transform var(--motion-fast) var(--ease-out), background-color var(--motion-fast) var(--ease-out);
}
.account-switch[aria-checked='true'] {
  background: var(--color-switch-on);
}
/* On, under the pointer: a step deeper (12.2). Off, busy and disabled look as they did. */
@media (hover: hover) {
  .account-switch[aria-checked='true']:not(:disabled):not([aria-busy='true']):hover {
    background: var(--color-switch-on-hover);
  }
}
.account-switch[aria-checked='true'] .account-switch__thumb {
  transform: translateX(12px);
  background: var(--color-switch-thumb);
}
.account-switch:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.account-switch[aria-busy='true'] {
  cursor: progress;
}
.account-switch:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
</style>
