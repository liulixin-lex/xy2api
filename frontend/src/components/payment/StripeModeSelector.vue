<template>
  <fieldset class="mt-4">
    <legend class="input-label">{{ t('admin.settings.payment.stripeMode') }}</legend>
    <div class="mt-1.5 flex flex-wrap gap-2">
      <label v-for="option in options" :key="option.value" class="flex min-h-10 cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors" :class="selected === option.value ? 'border-primary-500 bg-primary-50/40 text-gray-900 dark:bg-primary-900/10 dark:text-gray-100' : 'border-gray-300 text-gray-600 dark:border-dark-600 dark:text-gray-300'">
        <input type="radio" name="stripe-checkout-mode" :value="option.value" :checked="selected === option.value" class="h-4 w-4 accent-primary-600" @change="select(option.value)" />
        {{ option.label }}
      </label>
    </div>
    <p class="mt-2 text-xs leading-relaxed text-gray-500 dark:text-gray-400">{{ t('admin.settings.payment.stripeModeHint') }}</p>
  </fieldset>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ modelValue: string[] }>()
const emit = defineEmits<{ 'update:modelValue': [types: string[]] }>()
const { t } = useI18n()
const selected = computed(() => props.modelValue.includes('stripe_hosted') ? 'stripe_hosted' : props.modelValue.includes('stripe') ? 'stripe' : '')
const options = computed(() => [
  { value: '', label: t('admin.settings.payment.stripeModeOff') },
  { value: 'stripe', label: t('admin.settings.payment.providerStripe') },
  { value: 'stripe_hosted', label: t('admin.settings.payment.providerStripeHosted') },
])
function select(mode: string) {
  const types = props.modelValue.filter(type => type !== 'stripe' && type !== 'stripe_hosted')
  emit('update:modelValue', mode ? [...types, mode] : types)
}
</script>
