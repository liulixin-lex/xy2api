<template>
  <span ref="host" aria-hidden="true"></span>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { sanitizeSvg } from '@/utils/sanitize'

// An uploaded SVG drawn in its own shadow root. Its <style> (a custom menu icon on the live site carried
// `svg { color: … }`, which inlined into the page recoloured every icon) applies to the icon alone, and the icon still
// inherits the surrounding text colour for currentColor. The host's size classes set the icon's size.
const props = defineProps<{ svg: string }>()
const host = ref<HTMLElement | null>(null)

const FIT = '<style>:host { display: inline-block; } svg { display: block; width: 100%; height: 100%; }</style>'

function draw() {
  const el = host.value
  if (!el) return
  const root = el.shadowRoot ?? el.attachShadow({ mode: 'open' })
  root.innerHTML = FIT + sanitizeSvg(props.svg)
}

onMounted(draw)
watch(() => props.svg, draw)
</script>
