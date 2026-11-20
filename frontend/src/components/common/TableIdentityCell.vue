<template>
  <div
    v-tip="tip"
    class="table-identity"
    :class="{ 'table-identity--tap': cut }"
    data-test="table-identity"
    @click="reveal"
  >
    <span ref="primaryRef" class="table-identity__primary"><slot name="primary">{{ primary }}</slot></span>
    <span v-if="secondary || $slots.secondary" ref="secondaryRef" class="table-identity__secondary"><slot name="secondary">{{ secondary }}</slot></span>
  </div>
</template>

<script setup lang="ts">
/**
 * The frozen identity column's cell (mobile list plan): the main name on the first line, the secondary information on
 * the second, each cut with "…" at the column's width (on a phone clamp(120px, 36vw, 160px)). A cut line shows whole in
 * a tip on hover, or at once on a tap or click; `full` replaces the tip's text (for more than the two lines).
 */
import { computed, ref } from 'vue'
import { showTipNow, vTip } from '@/utils/tip'

const props = defineProps<{ primary?: string; secondary?: string; full?: string }>()

const primaryRef = ref<HTMLElement | null>(null)
const secondaryRef = ref<HTMLElement | null>(null)
const cut = ref(false)
const isCut = () => [primaryRef.value, secondaryRef.value].some((el) => !!el && el.scrollWidth > el.clientWidth + 1)
const tipText = computed(() => props.full || [props.primary, props.secondary].filter(Boolean).join('\n'))
/** The tip only when something is cut (or the page gave its own text): measured when it would show. */
const tip = () => {
  cut.value = isCut()
  return props.full || cut.value ? tipText.value : ''
}
const reveal = (event: MouseEvent) => {
  if (!props.full && !isCut()) return
  showTipNow(event.currentTarget)
}
</script>

<style scoped>
.table-identity {
  display: flex;
  min-width: 0;
  max-width: 100%;
  flex-direction: column;
}
.table-identity--tap {
  cursor: pointer;
}
.table-identity__primary,
.table-identity__secondary {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table-identity__primary {
  color: var(--color-text-primary);
  font-size: var(--font-size-name);
  font-weight: var(--font-weight-medium);
  line-height: var(--line-height-name);
}
.table-identity__secondary {
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
</style>
