<template>
  <div class="flex items-center gap-3" data-testid="share-global-switch">
    <div v-if="labelled" class="min-w-0 text-right">
      <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.global.title') }}</p>
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.global.hint') }}</p>
    </div>
    <Toggle
      :model-value="modelValue"
      :aria-label="t('admin.shareScheduling.global.title')"
      data-testid="share-global-toggle"
      :class="{ 'pointer-events-none opacity-50': disabled || saving }"
      @update:model-value="ask"
    />

    <BaseDialog :show="asking !== null" :title="asking ? t('admin.shareScheduling.global.onTitle') : t('admin.shareScheduling.global.offTitle')" width="narrow" @close="close">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ asking ? t('admin.shareScheduling.global.onBody') : t('admin.shareScheduling.global.offBody') }}</p>
      <p v-if="error" role="alert" class="mt-3 text-sm text-red-700 dark:text-red-300">{{ error }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('admin.shareScheduling.global.cancel') }}</button>
          <button
            type="button"
            class="btn"
            :class="asking ? 'btn-primary' : 'btn-danger'"
            :disabled="saving"
            data-testid="share-global-confirm"
            @click="confirm"
          >
            {{ saving ? t('admin.shareScheduling.global.saving') : asking ? t('admin.shareScheduling.global.turnOn') : t('admin.shareScheduling.global.turnOff') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

/**
 * The share scheduling main switch: every change is confirmed and saved at
 * once. Used in the page header and in system settings.
 */
const props = withDefaults(defineProps<{
  modelValue: boolean
  disabled?: boolean
  /** Shows the title and hint beside the toggle; off when the parent labels it. */
  labelled?: boolean
}>(), { disabled: false, labelled: true })
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

const { t } = useI18n()
/** The value being confirmed; null while no dialog is open. */
const asking = ref<boolean | null>(null)
const saving = ref(false)
const error = ref('')

function ask(next: boolean) {
  if (props.disabled || saving.value || next === props.modelValue) return
  error.value = ''
  asking.value = next
}

function close() {
  if (!saving.value) asking.value = null
}

async function confirm() {
  if (asking.value === null || saving.value) return
  const next = asking.value
  saving.value = true
  error.value = ''
  try {
    const saved = await shareSchedulingAPI.updateSettings({ enabled: next })
    emit('update:modelValue', typeof saved?.enabled === 'boolean' ? saved.enabled : next)
    asking.value = null
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.shareScheduling.global.failed'))
  } finally {
    saving.value = false
  }
}
</script>
