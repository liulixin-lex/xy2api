<template>
  <div class="space-y-6">
    <!-- Quick Amount Buttons: one radio group (arrow keys move and pick, only the picked amount is a Tab stop) -->
    <div v-if="filteredAmounts.length">
      <p :id="quickLabelId" class="mb-3 text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.quickAmounts') }}
      </p>
      <div
        role="radiogroup"
        :aria-labelledby="quickLabelId"
        class="grid grid-cols-3 gap-x-3 gap-y-4"
        @keydown="onRadioGroupKeydown"
      >
        <button
          v-for="amt in filteredAmounts"
          :key="amt"
          type="button"
          role="radio"
          :aria-checked="modelValue === amt"
          :tabindex="quickTabStop === amt ? 0 : -1"
          :class="[
            'relative min-h-12 rounded-md border px-2 py-3 text-center sm:px-4 text-sm tabular-nums transition-[color,background-color,border-color,box-shadow,transform] duration-150 ease-out active:scale-[0.97] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-900',
            modelValue === amt
              ? 'border-primary-500 bg-primary-50/40 font-semibold text-primary-700 inset-ring inset-ring-primary-500 dark:border-primary-400 dark:bg-primary-900/10 dark:text-primary-300 dark:inset-ring-primary-400'
              : quoteFor(amt).percent > 0
                ? 'border-red-200 bg-white font-medium text-gray-700 hover:border-red-300 hover:bg-gray-50 dark:border-red-500/40 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-red-400/60 dark:hover:bg-dark-700/60'
                : 'border-gray-200 bg-white font-medium text-gray-700 hover:border-gray-300 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500 dark:hover:bg-dark-700/60',
          ]"
          :data-testid="`quick-amount-${amt}`"
          @click="selectAmount(amt)"
        >
          <!-- 促销价签（单行）：仅命中档位的金额显示；红底白字、内圈点线、右侧圆孔、整体旋转 -->
          <span
            v-if="quoteFor(amt).percent > 0"
            class="pointer-events-none absolute -right-2 -top-3 z-10 rotate-12"
            data-testid="quick-amount-bonus-badge"
          >
            <span
              class="relative flex items-center gap-1 whitespace-nowrap rounded bg-red-600 py-0.5 pl-1.5 pr-1 text-[11px] font-extrabold leading-tight tracking-tight text-white shadow-md ring-2 ring-white before:pointer-events-none before:absolute before:inset-[2px] before:rounded-sm before:border before:border-dotted before:border-white/70 dark:bg-red-500 dark:ring-dark-800"
            >
              <span>{{ badgeText(amt) }}</span>
              <span class="h-1 w-1 shrink-0 rounded-full bg-white"></span>
            </span>
          </span>
          <span class="block">{{ amt }}</span>
          <!-- 配置了优惠阶梯时，所有按钮都显示第二行，保持高度一致：赠金显示到账 USD，折扣显示折后实付 -->
          <span
            v-if="showSecondLine"
            :class="[
              'mt-0.5 block text-[11px] font-normal leading-tight',
              quoteFor(amt).percent > 0 ? 'text-red-600 dark:text-red-300' : 'text-gray-400 dark:text-gray-500',
            ]"
            data-testid="quick-amount-credited"
          >{{ secondLine(amt) }}</span>
        </button>
      </div>
    </div>

    <!-- Custom Amount Input: the same amount as the quick buttons; an amount no method takes is flagged inside the field -->
    <div>
      <label :for="inputId" class="mb-3 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.customAmount') }}
      </label>
      <div
        :class="[
          'flex min-h-12 cursor-text items-center gap-3 rounded-xl border bg-white px-4 transition-[border-color,box-shadow] duration-150 ease-out focus-within:ring-2 dark:bg-dark-800',
          error
            ? 'border-status-back focus-within:ring-status-back/25'
            : 'border-gray-200 hover:border-gray-300 focus-within:border-primary-500 focus-within:ring-primary-500/30 dark:border-dark-600 dark:hover:border-dark-500 dark:focus-within:border-primary-500',
        ]"
        data-testid="amount-field"
        @click="inputRef?.focus()"
      >
        <span class="shrink-0 text-base text-gray-500 dark:text-dark-400" aria-hidden="true">
          {{ currencySymbol(currency) }}
        </span>
        <input
          :id="inputId"
          ref="inputRef"
          type="text"
          inputmode="decimal"
          autocomplete="off"
          enterkeyhint="go"
          :value="customText"
          :placeholder="placeholderText"
          :aria-invalid="error ? 'true' : undefined"
          :aria-describedby="errorId"
          class="h-12 min-w-0 flex-1 bg-transparent text-base font-medium tabular-nums text-gray-900 outline-none placeholder:font-normal placeholder:text-gray-500 dark:text-gray-100 dark:placeholder:text-dark-400"
          @input="handleInput"
          @keydown.enter="onEnter"
        />
        <span
          :id="errorId"
          aria-live="polite"
          class="max-w-[55%] shrink-0 text-right text-xs font-medium leading-tight text-status-back-text empty:hidden"
          data-testid="amount-error"
        >{{ error }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RechargeBonusTier } from '@/types/payment'
import { formatRechargeBonusNumber, quoteRechargeBonus, type RechargeBonusMode } from '@/utils/rechargeBonus'
import { currencySymbol, formatPaymentAmount } from './currency'
import { onRadioGroupKeydown } from './radioGroupKeys'

const props = withDefaults(defineProps<{
  amounts?: number[]
  modelValue: number | null
  min?: number
  max?: number
  /** 充值优惠阶梯（按 min_amount 升序）；为空时不显示价签与第二行 */
  bonusTiers?: RechargeBonusTier[]
  /** 阶梯模式：bonus 赠金 / discount 折扣 */
  bonusMode?: RechargeBonusMode
  /** 充值倍率（1 支付币种 = multiplier USD），用于计算到账金额 */
  multiplier?: number
  /** 支付币种（折扣模式第二行实付金额的币种与精度） */
  currency?: string
  /** 金额不可用时的就地提示（输入框内右侧）；空串表示金额可用 */
  error?: string
}>(), {
  amounts: () => [10, 20, 50, 100, 200, 500, 1000, 2000, 5000],
  min: 0,
  max: 0,
  bonusTiers: () => [],
  bonusMode: 'bonus',
  multiplier: 1,
  currency: 'CNY',
  error: '',
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
  /** Enter in the amount field: the page submits when the amount is payable */
  submit: []
}>()

const { t } = useI18n()

const uid = useId()
const quickLabelId = `quick-amounts-${uid}`
const inputId = `custom-amount-${uid}`
const errorId = `custom-amount-error-${uid}`
const inputRef = ref<HTMLInputElement | null>(null)

const customText = ref('')

// 0 = no limit
const filteredAmounts = computed(() =>
  props.amounts.filter((a) => (props.min <= 0 || a >= props.min) && (props.max <= 0 || a <= props.max))
)

// The quick amount that takes Tab focus: the picked one, or the first when the typed amount matches none.
const quickTabStop = computed(() =>
  props.modelValue !== null && filteredAmounts.value.includes(props.modelValue) ? props.modelValue : filteredAmounts.value[0]
)

const showSecondLine = computed(() => props.bonusTiers.length > 0)

function currencyDigits(): number {
  if (!props.currency) return 2
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency: props.currency }).resolvedOptions().maximumFractionDigits ?? 2
  } catch {
    return 2
  }
}

function quoteFor(amt: number) {
  return quoteRechargeBonus(props.bonusTiers, amt, {
    multiplier: props.multiplier,
    mode: props.bonusMode,
    currencyDigits: currencyDigits(),
  })
}

// 价签文案：赠金「+20%」，折扣「20% OFF」
function badgeText(amt: number): string {
  const percent = formatRechargeBonusNumber(quoteFor(amt).percent)
  return props.bonusMode === 'discount' ? `${percent}% OFF` : `+${percent}%`
}

function secondLine(amt: number): string {
  const quote = quoteFor(amt)
  if (props.bonusMode === 'discount') {
    return t('payment.rechargeBonus.payShort', { amount: formatPaymentAmount(quote.payBase, props.currency) })
  }
  return t('payment.rechargeBonus.creditedShort', { amount: '$' + quote.credited.toFixed(2) })
}

const placeholderText = computed(() => {
  if (props.min > 0 && props.max > 0) return `${props.min} - ${props.max}`
  if (props.min > 0) return `≥ ${props.min}`
  if (props.max > 0) return `≤ ${props.max}`
  return t('payment.enterAmount')
})

const AMOUNT_PATTERN = /^\d*(\.\d{0,2})?$/

function selectAmount(amt: number) {
  customText.value = String(amt)
  emit('update:modelValue', amt)
}

function onEnter(e: KeyboardEvent) {
  // Enter that confirms an IME candidate is not a submit.
  if (e.isComposing) return
  e.preventDefault()
  emit('submit')
}

function handleInput(e: Event) {
  const input = e.target as HTMLInputElement
  const val = input.value
  if (!AMOUNT_PATTERN.test(val)) {
    input.value = customText.value
    return
  }
  customText.value = val
  if (val === '') {
    emit('update:modelValue', null)
    return
  }
  const num = parseFloat(val)
  if (!isNaN(num) && num > 0) {
    emit('update:modelValue', num)
  } else {
    emit('update:modelValue', null)
  }
}

watch(() => props.modelValue, (v) => {
  if (v !== null && String(v) !== customText.value) {
    customText.value = String(v)
  }
}, { immediate: true })
</script>
