<template>
  <!-- Local switch so the master and field switches share one focus-visible style (m-5); the shared Toggle uses :focus. -->
  <button
    :id="id"
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :aria-label="ariaLabel"
    :aria-labelledby="ariaLabelledby"
    :aria-describedby="ariaDescribedby"
    :disabled="disabled"
    class="relative inline-flex flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transition-none dark:focus-visible:ring-offset-dark-800"
    :class="[large ? 'h-8 w-14' : 'h-6 w-11', modelValue ? 'bg-primary-600' : 'bg-gray-300 dark:bg-dark-600']"
    @click="emit('update:modelValue', !modelValue)"
  >
    <span
      class="pointer-events-none inline-block transform rounded-full bg-white shadow ring-0 transition duration-150 motion-reduce:transition-none"
      :class="[large ? 'h-7 w-7' : 'h-5 w-5', modelValue ? (large ? 'translate-x-6' : 'translate-x-5') : 'translate-x-0']"
    />
  </button>
</template>

<script setup lang="ts">
defineProps<{
  modelValue: boolean
  id?: string
  disabled?: boolean
  large?: boolean
  ariaLabel?: string
  ariaLabelledby?: string
  ariaDescribedby?: string
}>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()
</script>
