<template>
  <BaseDialog :show="show" :title="t('admin.shareScheduling.rebalance.title')" width="narrow" @close="close">
    <div class="space-y-4" data-testid="share-rebalance-dialog">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.rebalance.hint') }}</p>
      <fieldset class="space-y-3" :disabled="running">
        <legend class="sr-only">{{ t('admin.shareScheduling.rebalance.title') }}</legend>
        <label class="flex cursor-pointer items-start gap-3 rounded-xl border p-3" :class="mode === 'cold' ? 'border-primary-400 dark:border-primary-700' : 'border-gray-200 dark:border-dark-600'">
          <input v-model="mode" type="radio" name="share-rebalance-mode" value="cold" class="mt-1" data-testid="share-rebalance-cold">
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.rebalance.cold') }}</span>
            <span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.rebalance.coldHint') }}</span>
          </span>
        </label>
        <label class="flex cursor-pointer items-start gap-3 rounded-xl border p-3" :class="mode === 'all' ? 'border-red-400 dark:border-red-700' : 'border-gray-200 dark:border-dark-600'">
          <input v-model="mode" type="radio" name="share-rebalance-mode" value="all" class="mt-1" data-testid="share-rebalance-all">
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.rebalance.all') }}</span>
            <span class="block text-xs text-red-600 dark:text-red-300">{{ t('admin.shareScheduling.rebalance.allHint') }}</span>
          </span>
        </label>
      </fieldset>
      <p v-if="cleared !== null" role="status" class="text-sm text-emerald-700 dark:text-emerald-300" data-testid="share-rebalance-done">{{ t('admin.shareScheduling.rebalance.done', { count: cleared }) }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="running" @click="close">{{ t('common.close') }}</button>
        <button type="button" class="btn" :class="mode === 'all' ? 'btn-danger' : 'btn-primary'" :disabled="running" data-testid="share-rebalance-run" @click="run">
          {{ running ? t('admin.shareScheduling.rebalance.running') : t('admin.shareScheduling.rebalance.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { ShareRebalanceMode } from '@/api/admin/shareScheduling'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const store = useShareSchedulingStore()
const mode = ref<ShareRebalanceMode>('cold')
const running = ref(false)
const cleared = ref<number | null>(null)
const error = ref('')

watch(() => props.show, open => {
  if (open) {
    mode.value = 'cold'
    cleared.value = null
    error.value = ''
  }
})

function close() {
  if (!running.value) emit('close')
}

async function run() {
  if (running.value) return
  running.value = true
  cleared.value = null
  error.value = ''
  try {
    cleared.value = await store.rebalance(mode.value)
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.shareScheduling.rebalance.failed'))
  } finally {
    running.value = false
  }
}
</script>
