<script setup lang="ts">
defineProps<{
  colorClass: string
  tooltip?: string
  current: string | number
  max: string | number
  suffix?: string
  // 分组并发：有值时在 max 后面显示绿色的第三个数
  extra?: string | number | null
}>()
</script>

<template>
  <span
    :class="[
      'inline-flex items-center gap-1 rounded-md px-1.5 py-px text-[10px] font-medium leading-tight',
      colorClass
    ]"
    :title="tooltip"
  >
    <slot />
    <span class="font-mono">{{ current }}</span>
    <span class="text-gray-400 dark:text-gray-500">/</span>
    <span class="font-mono">{{ max }}</span>
    <template v-if="extra != null">
      <span class="text-gray-400 dark:text-gray-500">/</span>
      <span class="font-mono text-emerald-600 dark:text-emerald-400" data-test="capacity-group-current">{{ extra }}</span>
    </template>
    <span v-if="suffix" class="text-[9px] opacity-60">{{ suffix }}</span>
  </span>
</template>
