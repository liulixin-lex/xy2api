<template>
  <form class="space-y-4" data-testid="ticket-create-form" @submit.prevent="submit">
    <p
      v-if="summary?.notice"
      class="whitespace-pre-wrap rounded-xl bg-primary-50 px-3 py-2 text-sm text-primary-800 dark:bg-primary-900/30 dark:text-primary-200"
      data-testid="ticket-notice"
    >{{ summary.notice }}</p>
    <p
      v-if="limitReached"
      class="rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
      data-testid="ticket-open-limit"
    >{{ t('supportTickets.form.openLimitReached', { max: summary?.max_open }) }}</p>

    <div v-if="categories.length">
      <label class="input-label" :for="`${id}-category`">{{ t('supportTickets.form.category') }}</label>
      <Select
        :id="`${id}-category`"
        v-model="category"
        :options="categoryOptions"
        :placeholder="t('supportTickets.form.categoryPlaceholder')"
        data-testid="ticket-category"
      />
    </div>
    <div>
      <label class="input-label" :for="`${id}-title`">{{ t('supportTickets.form.subject') }}</label>
      <input
        :id="`${id}-title`"
        v-model="title"
        class="input"
        :maxlength="SUPPORT_TICKET_TITLE_MAX"
        :placeholder="t('supportTickets.form.subjectPlaceholder')"
        data-testid="ticket-title"
      />
    </div>
    <div>
      <label class="input-label" :for="`${id}-body`">{{ t('supportTickets.form.body') }}</label>
      <textarea
        :id="`${id}-body`"
        v-model="body"
        :rows="compact ? 5 : 7"
        class="input resize-y"
        :placeholder="t('supportTickets.form.bodyPlaceholder')"
        data-testid="ticket-body"
      ></textarea>
      <div class="mt-1 flex items-start justify-between gap-3 text-xs">
        <span class="text-amber-700 dark:text-amber-300">{{ t('supportTickets.form.secretWarning') }}</span>
        <span class="shrink-0" :class="bodyTooLong ? 'font-medium text-red-600 dark:text-red-400' : 'text-gray-400'" data-testid="ticket-body-counter">
          {{ t('supportTickets.form.counter', { count: bodyLength, max: SUPPORT_TICKET_BODY_MAX }) }}
        </span>
      </div>
    </div>
    <div class="flex justify-end gap-2">
      <button type="button" class="btn btn-secondary" @click="emit('cancel')">{{ t('common.cancel') }}</button>
      <button type="submit" class="btn btn-primary" :disabled="!canSubmit" data-testid="ticket-submit">
        {{ submitting ? t('supportTickets.form.submitting') : t('supportTickets.form.submit') }}
      </button>
    </div>
  </form>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import {
  createTicket,
  SUPPORT_TICKET_BODY_MAX,
  SUPPORT_TICKET_TITLE_MAX,
  supportTicketLength,
  type SupportTicketDetail,
  type SupportTicketUserSummary,
} from '@/api/supportTickets'
import { useAppStore } from '@/stores/app'
import { supportTicketErrorMessage } from './supportTickets'

// active: the form is on screen; it starts empty every time it becomes active.
const props = withDefaults(defineProps<{ summary: SupportTicketUserSummary | null; active?: boolean; compact?: boolean }>(), { active: true, compact: false })
const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'created', detail: SupportTicketDetail): void
}>()
const { t } = useI18n()
const appStore = useAppStore()
const id = `support-ticket-${useId()}`

const category = ref('')
const title = ref('')
const body = ref('')
const submitting = ref(false)

const categories = computed(() => props.summary?.categories ?? [])
const categoryOptions = computed(() => categories.value.map((name) => ({ value: name, label: name })))
const limitReached = computed(() => !!props.summary && props.summary.open_count >= props.summary.max_open)
const bodyLength = computed(() => supportTicketLength(body.value))
const bodyTooLong = computed(() => bodyLength.value > SUPPORT_TICKET_BODY_MAX)
const canSubmit = computed(() =>
  !submitting.value &&
  !limitReached.value &&
  (categories.value.length === 0 || category.value !== '') &&
  title.value.trim() !== '' &&
  body.value.trim() !== '' &&
  !bodyTooLong.value
)

watch(() => props.active, (active) => {
  if (active) {
    category.value = ''
    title.value = ''
    body.value = ''
  }
})

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  try {
    const detail = await createTicket({ category: category.value, title: title.value.trim(), body: body.value.trim() })
    appStore.showSuccess(t('supportTickets.form.created'))
    emit('created', detail)
  } catch (err) {
    appStore.showError(supportTicketErrorMessage(err, t))
  } finally {
    submitting.value = false
  }
}
</script>
