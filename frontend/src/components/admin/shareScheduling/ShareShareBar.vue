<template>
  <div class="flex items-center gap-2" :title="text">
    <div class="h-2 min-w-[4rem] flex-1 rounded-full bg-gray-100 dark:bg-dark-700" aria-hidden="true">
      <div class="h-2 rounded-full" :class="barClass" :style="{ width: width }" />
    </div>
    <span class="w-14 shrink-0 text-right text-sm tabular-nums" :class="value > 0 ? 'text-gray-900 dark:text-white' : 'text-gray-400 dark:text-dark-500'">{{ text }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatSharePercent } from '@/utils/shareScheduling'

/** A share (0..1) as a bar with its percentage; zero shows a dash. */
const props = withDefaults(defineProps<{ value: number; barClass?: string }>(), { barClass: 'bg-primary-500' })

// Tiny shares keep two decimals so they never read as 0.0%.
const text = computed(() => (props.value > 0 ? formatSharePercent(props.value, props.value < 0.01 ? 2 : 1) : '—'))
const width = computed(() => `${Math.min(Math.max(props.value, 0), 1) * 100}%`)
</script>
