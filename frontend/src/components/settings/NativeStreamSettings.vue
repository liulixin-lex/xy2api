<template>
  <section class="scheduling-card" aria-labelledby="native-stream-title">
    <h2 id="native-stream-title" class="font-semibold">{{ t('admin.scheduling.nativeStream.title') }}</h2>
    <p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeStream.snapshotHint') }}</p>
    <div class="mt-4 space-y-4">
      <div>
        <label class="inline-flex items-center gap-2 text-sm font-medium">
          <input type="checkbox" :checked="value.delivery" data-testid="native-delivery" aria-describedby="native-delivery-hint" @change="setDelivery">
          {{ t('admin.scheduling.nativeStream.delivery') }}
        </label>
        <p id="native-delivery-hint" class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeStream.deliveryHint') }}</p>
      </div>
      <div>
        <label class="inline-flex items-center gap-2 text-sm font-medium">
          <input type="checkbox" :checked="value.recovery" :disabled="!value.delivery" data-testid="native-recovery" aria-describedby="native-recovery-hint" @change="setRecovery">
          {{ t('admin.scheduling.nativeStream.recovery') }}
        </label>
        <p id="native-recovery-hint" class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeStream.recoveryHint') }}</p>
      </div>
      <div>
        <label class="inline-flex items-center gap-2 text-sm font-medium">
          <input type="checkbox" :checked="false" disabled data-testid="native-persistence" aria-describedby="native-persistence-hint">
          {{ t('admin.scheduling.nativeStream.persistence') }}
        </label>
        <p id="native-persistence-hint" class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeStream.persistenceHint') }}</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { NativeStreamFeatures } from '@/types/scheduling'
const props = defineProps<{ modelValue?: NativeStreamFeatures }>()
const emit = defineEmits<{ 'update:modelValue': [value: NativeStreamFeatures] }>()
const { t } = useI18n()
const value = computed(() => props.modelValue ?? { delivery: false, recovery: false, persistence: false })
function setDelivery(event: Event) {
  const delivery = (event.target as HTMLInputElement).checked
  emit('update:modelValue', { delivery, recovery: delivery && value.value.recovery, persistence: false })
}
function setRecovery(event: Event) {
  emit('update:modelValue', { ...value.value, recovery: value.value.delivery && (event.target as HTMLInputElement).checked, persistence: false })
}
</script>
