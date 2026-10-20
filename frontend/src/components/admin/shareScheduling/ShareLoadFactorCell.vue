<template>
  <div class="min-w-[6rem]" :data-testid="'share-load-factor-' + member.account_id">
    <div class="flex items-center gap-1.5">
      <input
        v-if="editing"
        ref="inputRef"
        v-model="input"
        type="text"
        inputmode="numeric"
        class="input h-8 px-2 py-0 text-sm tabular-nums"
        :class="member.load_factor === null ? 'w-48' : 'w-20'"
        :placeholder="member.load_factor === null ? unsetHint : ''"
        :aria-label="t('admin.shareScheduling.loadFactor.edit', { name: member.name })"
        :data-testid="'share-load-factor-input-' + member.account_id"
        @keydown.enter.prevent="commit"
        @keydown.esc.prevent="cancel"
        @blur="commit"
      >
      <button
        v-else
        type="button"
        class="inline-flex h-8 min-w-[3rem] items-center justify-center rounded-lg border border-transparent px-2 text-sm tabular-nums transition-colors hover:border-gray-200 hover:bg-gray-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:hover:border-dark-600 dark:hover:bg-dark-700/60"
        :class="member.load_factor === null ? 'text-gray-400 dark:text-dark-400' : 'font-medium text-gray-900 dark:text-white'"
        :title="member.load_factor === null ? unsetHint : t('admin.shareScheduling.loadFactor.edit', { name: member.name })"
        :aria-label="t('admin.shareScheduling.loadFactor.edit', { name: member.name })"
        :disabled="saving"
        :data-testid="'share-load-factor-value-' + member.account_id"
        @click="start"
      >
        {{ member.effective_load_factor }}
        <span v-if="saving" class="ml-1 h-1.5 w-1.5 animate-pulse rounded-full bg-primary-500" aria-hidden="true" />
      </button>
      <span
        v-if="otherGroups.length"
        class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[11px] text-gray-600 dark:bg-dark-700 dark:text-dark-300"
        :title="t('admin.shareScheduling.loadFactor.alsoAffects', { groups: otherGroups.join('、') })"
      >{{ t('admin.shareScheduling.loadFactor.otherGroups', { count: otherGroups.length }) }}</span>
    </div>
    <p v-if="editing" class="mt-1 max-w-[14rem] text-[11px] leading-snug text-gray-500 dark:text-dark-400">
      {{ t('admin.shareScheduling.loadFactor.clearHint') }}<template v-if="otherGroups.length">；<span class="text-amber-700 dark:text-amber-300" :data-testid="'share-load-factor-affects-' + member.account_id">{{ t('admin.shareScheduling.loadFactor.alsoAffects', { groups: otherGroups.join('、') }) }}</span></template>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { update as updateAccount } from '@/api/admin/accounts'
import type { ShareMember } from '@/api/admin/shareScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'
import { SHARE_MAX_LOAD_FACTOR, SHARE_MIN_LOAD_FACTOR } from '@/utils/shareScheduling'

/**
 * Inline editor of an account's load factor. The load factor belongs to the
 * account, so a change reaches every group the account serves; the editor
 * names those groups while it is open.
 */
const props = defineProps<{
  member: ShareMember
  otherGroups: string[]
}>()
const emit = defineEmits<{
  (e: 'updated', value: number | null): void
  (e: 'error', message: string): void
}>()

const { t } = useI18n()
/** An unset load factor counts as the concurrency limit (the effective value). */
const unsetHint = computed(() => t('admin.shareScheduling.loadFactor.fromConcurrency', { value: props.member.effective_load_factor }))
const editing = ref(false)
const saving = ref(false)
const input = ref('')
const inputRef = ref<HTMLInputElement | null>(null)

async function start() {
  if (saving.value) return
  input.value = props.member.load_factor === null ? '' : String(props.member.load_factor)
  editing.value = true
  await nextTick()
  inputRef.value?.focus()
  inputRef.value?.select()
}

function cancel() {
  editing.value = false
}

/** Parses the input: null clears the load factor, undefined is invalid. */
function parse(raw: string): number | null | undefined {
  const text = raw.trim()
  if (text === '') return null
  if (!/^\d+$/.test(text)) return undefined
  const value = Number(text)
  return value >= SHARE_MIN_LOAD_FACTOR && value <= SHARE_MAX_LOAD_FACTOR ? value : undefined
}

async function commit() {
  if (!editing.value) return
  editing.value = false
  const next = parse(input.value)
  if (next === undefined) {
    emit('error', t('admin.shareScheduling.loadFactor.invalid', { max: SHARE_MAX_LOAD_FACTOR }))
    return
  }
  if (next === props.member.load_factor) return
  saving.value = true
  try {
    // The update endpoint only touches the fields sent; 0 clears the load factor.
    await updateAccount(props.member.account_id, { load_factor: next ?? 0 })
    emit('updated', next)
  } catch (err) {
    emit('error', extractApiErrorMessage(err, t('admin.shareScheduling.loadFactor.failed')))
  } finally {
    saving.value = false
  }
}
</script>
