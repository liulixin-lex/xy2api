<template>
  <div class="space-y-6" data-test="charts-panel">
    <ChartsFilterBar :state="state" :now="now" />

    <!-- Only a current answer: after a failed read the strip would repeat old numbers (R M2-U-3). -->
    <ExperimentStrip v-if="state.experiments.status === 'ready' && state.experiments.data" :experiments="state.experiments.data.experiments" @open="openExperiments" />

    <NorthStarCard :state="state" />

    <div class="grid gap-6 xl:grid-cols-3">
      <LossWaterfallCard class="xl:col-span-2" :state="state" @open-settings="emit('open-settings')" />
      <CoverageCard :state="state" @open-settings="emit('open-settings')" />
    </div>

    <BreakdownCard :state="state" />

    <div class="grid gap-6 lg:grid-cols-2">
      <IdentityCard :state="state" @open-settings="emit('open-settings')" />
      <RoutingCard :state="state" @open-settings="emit('open-settings')" />
    </div>

    <div id="experiments" ref="experimentsAnchor">
      <ExperimentSection :state="state" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import type { CacheChartsState } from './useCacheCharts'
import ChartsFilterBar from './components/ChartsFilterBar.vue'
import ExperimentStrip from './components/ExperimentStrip.vue'
import NorthStarCard from './components/NorthStarCard.vue'
import LossWaterfallCard from './components/LossWaterfallCard.vue'
import CoverageCard from './components/CoverageCard.vue'
import BreakdownCard from './components/BreakdownCard.vue'
import IdentityCard from './components/IdentityCard.vue'
import RoutingCard from './components/RoutingCard.vue'
import ExperimentSection from './components/ExperimentSection.vue'

const props = defineProps<{ state: CacheChartsState }>()
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const now = ref(new Date())
const experimentsAnchor = ref<HTMLElement | null>(null)
let ticker: ReturnType<typeof setInterval> | null = null

async function openExperiments() {
  await nextTick()
  const anchor = experimentsAnchor.value
  if (!anchor) return
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  anchor.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
  const heading = anchor.querySelector<HTMLElement>('h2')
  if (heading) {
    heading.setAttribute('tabindex', '-1')
    heading.focus({ preventScroll: true })
  }
}

onMounted(() => {
  ticker = setInterval(() => {
    now.value = new Date()
  }, 60_000)
})

onBeforeUnmount(() => {
  if (ticker) clearInterval(ticker)
  props.state.stopAutoRefresh()
})
</script>
