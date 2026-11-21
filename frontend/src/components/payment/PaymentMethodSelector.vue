<template>
  <div>
    <p :id="labelId" class="mb-3 text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ t('payment.paymentMethod') }}
    </p>
    <!-- One radio group: arrow keys move and pick among the usable methods; only the picked one is a Tab stop -->
    <div
      data-testid="payment-method-grid"
      role="radiogroup"
      :aria-labelledby="labelId"
      class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4"
      @keydown="onRadioGroupKeydown"
    >
      <button
        v-for="method in sortedMethods"
        :key="method.type"
        type="button"
        role="radio"
        :title="methodLabel(method)"
        :disabled="!method.available"
        :aria-checked="selected === method.type"
        :tabindex="tabStop === method.type ? 0 : -1"
        :class="[
          'relative flex h-14 min-w-0 flex-col items-center justify-center rounded-md border px-3 transition-[color,background-color,border-color,box-shadow,transform] duration-150 ease-out enabled:active:scale-[0.97] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-900',
          !method.available
            ? 'cursor-not-allowed border-gray-200 bg-gray-50 opacity-50 dark:border-dark-700 dark:bg-dark-800/50'
            : selected === method.type
              ? 'border-primary-500 bg-primary-50/40 text-gray-900 inset-ring inset-ring-primary-500 dark:border-primary-400 dark:bg-primary-900/10 dark:text-gray-100 dark:inset-ring-primary-400'
              : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500 dark:hover:bg-dark-700/60',
        ]"
        @click="method.available && emit('select', method.type)"
      >
        <span class="flex w-full min-w-0 items-center justify-center gap-2">
          <img :src="methodIcon(method.type)" alt="" class="h-6 w-6 shrink-0 object-contain" />
          <span class="flex min-w-0 flex-col items-start leading-none">
            <span data-testid="payment-method-label" class="block w-full truncate text-sm font-medium leading-tight">
              {{ methodLabel(method) }}
            </span>
            <span
              v-if="method.fee_rate > 0"
              class="mt-1 text-xs tabular-nums text-gray-500 dark:text-dark-400"
            >
              {{ t('payment.fee') }} {{ method.fee_rate }}%
            </span>
          </span>
        </span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { METHOD_ORDER, isBuiltInAlipayMethod, isBuiltInWxpayMethod } from './providerConfig'
import { onRadioGroupKeydown } from './radioGroupKeys'
import alipayIcon from '@/assets/icons/alipay.svg'
import wxpayIcon from '@/assets/icons/wxpay.svg'
import stripeIcon from '@/assets/icons/stripe.svg'
import airwallexIcon from '@/assets/icons/airwallex.svg'
import paymentIcon from '@/assets/icons/payment.svg'

export interface PaymentMethodOption {
  type: string
  display_name?: string
  fee_rate: number
  available: boolean
}

const props = defineProps<{
  methods: PaymentMethodOption[]
  selected: string
}>()

const emit = defineEmits<{
  select: [type: string]
}>()

const { t } = useI18n()

const labelId = `payment-method-${useId()}`

const METHOD_ICONS: Record<string, string> = {
  alipay: alipayIcon,
  wxpay: wxpayIcon,
  stripe: stripeIcon,
  stripe_hosted: stripeIcon,
  airwallex: airwallexIcon,
  credit_card: paymentIcon,
}

const sortedMethods = computed(() => {
  const order: readonly string[] = METHOD_ORDER
  return [...props.methods].sort((a, b) => {
    const ai = order.indexOf(a.type)
    const bi = order.indexOf(b.type)
    return (ai === -1 ? 999 : ai) - (bi === -1 ? 999 : bi)
  })
})

// The method that takes Tab focus: the picked one when usable, else the first usable one.
const tabStop = computed(() => {
  const usable = sortedMethods.value.filter((method) => method.available)
  return usable.find((method) => method.type === props.selected)?.type ?? usable[0]?.type
})

function methodIcon(type: string): string {
  if (isBuiltInAlipayMethod(type)) return METHOD_ICONS.alipay
  if (isBuiltInWxpayMethod(type)) return METHOD_ICONS.wxpay
  if (type === 'airwallex') return METHOD_ICONS.airwallex
  return METHOD_ICONS[type] || paymentIcon
}

function methodLabel(method: PaymentMethodOption): string {
  if (method.type === 'stripe' || method.type === 'stripe_hosted') return 'Stripe'
  return method.display_name || t(`payment.methods.${method.type}`, method.type)
}
</script>
