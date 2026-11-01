<template>
  <div v-roving-tabs class="tabs inline-flex" role="tablist" :aria-label="label">
    <button
      v-for="tab in tabs"
      :key="tab.value"
      type="button"
      role="tab"
      class="tab flex-1 whitespace-nowrap sm:flex-none"
      :class="modelValue === tab.value ? 'tab-active' : ''"
      :aria-selected="modelValue === tab.value"
      :data-testid="`support-tab-${tab.value}`"
      @click="emit('update:modelValue', tab.value)"
    >
      {{ tab.label }}
      <span
        v-if="tab.badge"
        class="ml-1.5 inline-flex min-w-5 items-center justify-center rounded-full bg-red-600 px-1.5 text-[10px] font-semibold leading-5 text-white"
      >{{ tab.badge > 99 ? '99+' : tab.badge }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { vRovingTabs } from '@/utils/rovingTabs'

defineProps<{ tabs: Array<{ value: string; label: string; badge?: number }>; modelValue: string; label: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()
</script>
