<template>
  <!-- Kept to the cell: the label is cut (the tip has it whole), the clear button never shrinks or leaves the cell. -->
  <div v-if="entries.length" class="mt-1 flex min-w-0 max-w-full items-center gap-1" data-test="stream-only-badge" :title="`${label}\n${title}`">
    <span class="min-w-0 truncate rounded bg-sky-50 px-1.5 py-0.5 text-[11px] font-medium leading-4 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300" data-test="stream-only-label">
      {{ label }}
    </span>
    <button
      v-if="canManage"
      type="button"
      class="shrink-0 rounded px-1 text-[11px] leading-4 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 disabled:opacity-50 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-200"
      :disabled="clearing"
      :aria-label="t('admin.resilience.capability.clearTitle')"
      :title="t('admin.resilience.capability.clearTitle')"
      data-test="stream-only-clear"
      @click="clear"
    >{{ t('admin.resilience.capability.clear') }}</button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { clearCapabilityLearning } from '@/api/admin/accountCapability'
import { activeStreamOnly, streamOnlyProtocols, streamOnlyRemaining, type AccountWithCapability } from '@/utils/accountCapability'
import { useAppStore } from '@/stores/app'
import { useAdminPermission } from '@/composables/useAdminPermission'

const props = defineProps<{ account: AccountWithCapability }>()
const emit = defineEmits<{ (e: 'cleared', account: AccountWithCapability): void }>()

const { t } = useI18n()
const appStore = useAppStore()
// The same check as the quality panel's clear buttons: a read-only operator sees the state, not the button.
const { canManage } = useAdminPermission('accounts')
const clearing = ref(false)

// Expiry does not touch the account row, so the list's auto refresh keeps the
// row; a minute tick, only while something is learned, counts down and hides it.
const now = ref(Date.now())
const entries = computed(() => activeStreamOnly(props.account.capability_learned, now.value))
let timer: ReturnType<typeof setInterval> | undefined

watch(() => entries.value.length > 0, (shown) => {
  if (shown && !timer) {
    timer = setInterval(() => { now.value = Date.now() }, 60_000)
  } else if (!shown && timer) {
    clearInterval(timer)
    timer = undefined
  }
}, { immediate: true })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })

const remaining = computed(() => {
  const { unit, n } = streamOnlyRemaining(entries.value, now.value)
  return t(`admin.resilience.capability.${unit}`, { n })
})
const label = computed(() => t('admin.resilience.capability.badge', { time: remaining.value }))

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
