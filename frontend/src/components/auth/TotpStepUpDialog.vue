<template>
  <Teleport to="body">
    <div
      v-if="controller.visible.value"
      ref="rootRef"
      class="fixed inset-0 overflow-y-auto"
      :style="{ zIndex: layer.zIndex.value }"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="titleId"
      data-step-up-dialog
      @keydown.tab="layer.trapTab"
    >
      <div class="flex min-h-full items-center justify-center p-4">
        <div class="fixed inset-0 bg-black/50 transition-opacity" @click="handleCancel"></div>

        <div ref="panelRef" tabindex="-1" class="relative w-full max-w-md transform rounded-xl bg-white p-6 shadow-xl transition-all focus:outline-none dark:bg-dark-800">
          <div class="mb-6 text-center">
            <div class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary-100 dark:bg-primary-900/30">
              <svg class="h-6 w-6 text-primary-600 dark:text-primary-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 10-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 002.25-2.25v-6.75a2.25 2.25 0 00-2.25-2.25H6.75a2.25 2.25 0 00-2.25 2.25v6.75a2.25 2.25 0 002.25 2.25z" />
              </svg>
            </div>
            <h3 :id="titleId" class="mt-4 text-xl font-semibold text-gray-900 dark:text-white">
              {{ t('stepUp.title') }}
            </h3>
            <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
              {{ t('stepUp.hint') }}
            </p>
          </div>

          <div class="mb-6">
            <input
              ref="hiddenOtpInputRef"
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength="6"
              class="pointer-events-none absolute left-0 top-0 h-px w-px opacity-0"
              aria-hidden="true"
              tabindex="-1"
              @input="handleHiddenOtpInput"
            />
            <div class="flex justify-center gap-2">
              <input
                v-for="(_, index) in 6"
                :key="index"
                :ref="(el) => setInputRef(el, index)"
                type="text"
                maxlength="1"
                inputmode="numeric"
                pattern="[0-9]"
                autocomplete="off"
                class="h-12 w-10 rounded-lg border border-gray-300 text-center text-lg font-semibold focus:border-primary-500 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
                :disabled="verifying"
                @input="handleCodeInput($event, index)"
                @keydown="handleKeydown($event, index)"
                @paste="handlePaste"
              />
            </div>
            <div v-if="verifying" class="mt-3 flex items-center justify-center gap-2 text-sm text-gray-500">
              <div class="animate-spin rounded-full h-4 w-4 border-b-2 border-primary-500"></div>
              {{ t('common.verifying') }}
            </div>
          </div>

          <button
            type="button"
            class="btn btn-secondary w-full"
            :disabled="verifying"
            @click="handleCancel"
          >
            {{ t('common.cancel') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import { totpAPI } from '@/api'
import type { StepUpController } from '@/composables/useStepUp'
import { useModalLayer } from '@/composables/useModalLayer'

const props = defineProps<{
  controller: StepUpController
}>()

const { t } = useI18n()
const appStore = useAppStore()

const verifying = ref(false)
const code = ref<string[]>(['', '', '', '', '', ''])
const inputRefs = ref<(HTMLInputElement | null)[]>([])
const hiddenOtpInputRef = ref<HTMLInputElement | null>(null)

const titleId = `step-up-title-${useId()}`
const rootRef = ref<HTMLElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)

watch(
  () => props.controller.visible.value,
  (open) => {
    if (open) resetInputs()
  }
)

// The dialog is often asked for from inside another dialog: it is the top modal layer of the overlay stack, takes
// focus on its first cell and gives it back to the layer below when it closes; Escape cancels.
const layer = useModalLayer(() => props.controller.visible.value, {
  root: rootRef,
  panel: panelRef,
  floor: 60,
  initialFocus: () => inputRefs.value[0],
  onEscape: () => handleCancel()
})

// Auto-submit once 6 digits are entered.
watch(
  () => code.value.join(''),
  (newCode) => {
    if (newCode.length === 6 && !verifying.value) {
      submit(newCode)
    }
  }
)

async function submit(otp: string) {
  verifying.value = true
  try {
    await totpAPI.stepUp(otp)
    verifying.value = false
    resetInputs()
    props.controller.onVerified()
  } catch (err: any) {
    verifying.value = false
    appStore.showError(err?.message || t('stepUp.verifyFailed'))
    resetInputs()
    nextTick(() => inputRefs.value[0]?.focus())
  }
}

function resetInputs() {
  code.value = ['', '', '', '', '', '']
  inputRefs.value.forEach((input) => {
    if (input) input.value = ''
  })
  if (hiddenOtpInputRef.value) hiddenOtpInputRef.value.value = ''
}

function handleCancel() {
  if (verifying.value) return
  props.controller.onCancel()
}

const setInputRef = (el: any, index: number) => {
  inputRefs.value[index] = el as HTMLInputElement | null
}

const handleCodeInput = (event: Event, index: number) => {
  const input = event.target as HTMLInputElement
  const value = input.value.replace(/[^0-9]/g, '')
  code.value[index] = value
  if (value && index < 5) {
    nextTick(() => inputRefs.value[index + 1]?.focus())
  }
}

const handleHiddenOtpInput = (event: Event) => {
  const input = event.target as HTMLInputElement
  const digits = input.value.replace(/[^0-9]/g, '').slice(0, 6).split('')
  for (let i = 0; i < 6; i++) {
    code.value[i] = digits[i] || ''
    if (inputRefs.value[i]) inputRefs.value[i]!.value = digits[i] || ''
  }
}

const handleKeydown = (event: KeyboardEvent, index: number) => {
  if (event.key === 'Backspace') {
    const input = event.target as HTMLInputElement
    if (!input.value && index > 0) {
      event.preventDefault()
      inputRefs.value[index - 1]?.focus()
    }
  }
}

const handlePaste = (event: ClipboardEvent) => {
  event.preventDefault()
  const pastedData = event.clipboardData?.getData('text') || ''
  const digits = pastedData.replace(/[^0-9]/g, '').slice(0, 6).split('')
  for (let i = 0; i < 6; i++) {
    code.value[i] = digits[i] || ''
    if (inputRefs.value[i]) inputRefs.value[i]!.value = digits[i] || ''
  }
  const focusIndex = Math.min(digits.length, 5)
  nextTick(() => inputRefs.value[focusIndex]?.focus())
}
</script>
