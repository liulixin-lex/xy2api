<template>
  <div v-if="entries.length" class="mt-1 flex items-center gap-1" data-test="stream-only-badge" :title="title">
    <span class="rounded bg-sky-50 px-1.5 py-0.5 text-[11px] font-medium leading-4 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300">
      {{ t('admin.resilience.capability.badge', { time: remaining }) }}
    </span>
    <button
      type="button"
      class="rounded px-1 text-[11px] leading-4 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 disabled:opacity-50 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-200"
      :disabled="clearing"
      :aria-label="t('admin.resilience.capability.clearTitle')"
      :title="t('admin.resilience.capability.clearTitle')"
      data-test="stream-only-clear"
      @click="clear"
    >{{ t('admin.resilience.capability.clear') }}</button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { clearCapabilityLearning } from '@/api/admin/accountCapability'
import { activeStreamOnly, streamOnlyProtocols, streamOnlyRemaining, type AccountWithCapability } from '@/utils/accountCapability'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ account: AccountWithCapability }>()
const emit = defineEmits<{ (e: 'cleared', account: AccountWithCapability): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const clearing = ref(false)

// The list refreshes itself, so the time left is read when the row renders.
const entries = computed(() => activeStreamOnly(props.account.capability_learned))

const remaining = computed(() => {
  const { unit, n } = streamOnlyRemaining(entries.value)
  return t(`admin.resilience.capability.${unit}`, { n })
})

const title = computed(() => t('admin.resilience.capability.title', { protocols: streamOnlyProtocols(entries.value) }))

async function clear() {
  if (clearing.value) return
  clearing.value = true
  try {
    await clearCapabilityLearning(props.account.id)
    emit('cleared', { ...props.account, capability_learned: [] })
    appStore.showSuccess(t('admin.resilience.capability.cleared'))
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('admin.resilience.capability.clearFailed'))
  } finally {
    clearing.value = false
  }
}
</script>
