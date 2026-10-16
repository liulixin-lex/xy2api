<template>
  <BaseDialog :show="preview !== null" :title="t('admin.shareScheduling.preview.title')" width="wide" @close="emit('close')">
    <div v-if="preview" class="space-y-5" data-testid="share-preview">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-700/60">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.preview.placementChange') }}</p>
          <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white" data-testid="share-preview-placement">{{ formatSharePercent(preview.placement_change) }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.preview.placementChangeHint', { count: preview.sampled_keys }) }}</p>
        </div>
        <div class="rounded-xl p-4" :class="preview.estimated_migrations > 0 ? 'bg-amber-50 dark:bg-amber-900/20' : 'bg-gray-50 dark:bg-dark-700/60'">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.preview.migrations') }}</p>
          <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white" data-testid="share-preview-migrations">{{ preview.estimated_migrations }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.preview.migrationsHint') }}</p>
          <p v-if="preview.deadline_accounts?.length" class="mt-1 text-xs text-amber-700 dark:text-amber-300">{{ t('admin.shareScheduling.preview.deadlineAccounts', { ids: preview.deadline_accounts.map(id => '#' + id).join(', ') }) }}</p>
        </div>
      </div>

      <div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.preview.warningsTitle') }}</h4>
        <ul v-if="preview.warnings.length" class="mt-2 space-y-1 text-sm text-amber-800 dark:text-amber-200" data-testid="share-preview-warnings">
          <li v-for="(warning, index) in preview.warnings" :key="index" class="rounded-lg bg-amber-50 px-3 py-2 dark:bg-amber-900/20">{{ warningText(warning) }}</li>
        </ul>
        <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.preview.noWarnings') }}</p>
      </div>

      <div v-if="changes.length">
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.preview.changes') }}</h4>
        <div class="mt-2 max-h-72 overflow-auto">
          <table class="w-full text-left text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="p-2">{{ t('admin.shareScheduling.preview.columns.account') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.preview.columns.before') }}</th>
                <th class="p-2 text-right">{{ t('admin.shareScheduling.preview.columns.after') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="change in changes" :key="change.id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                <td class="p-2">{{ change.name }} <span class="text-xs text-gray-500 dark:text-dark-400">#{{ change.id }}</span></td>
                <td class="p-2 text-right tabular-nums">{{ formatSharePercent(change.before) }}</td>
                <td class="p-2 text-right tabular-nums" :class="change.after > change.before ? 'text-emerald-700 dark:text-emerald-300' : 'text-red-700 dark:text-red-300'">{{ formatSharePercent(change.after) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('admin.shareScheduling.preview.back') }}</button>
        <button type="button" class="btn btn-primary" :disabled="saving" data-testid="share-preview-confirm" @click="emit('confirm')">{{ saving ? t('admin.shareScheduling.policy.saving') : t('admin.shareScheduling.preview.confirm') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { SharePreview, ShareWarning } from '@/api/admin/shareScheduling'
import type { ShareGroupEntry } from '@/stores/shareScheduling'
import { formatSharePercent } from '@/utils/shareScheduling'

const props = defineProps<{
  entry: ShareGroupEntry
  preview: SharePreview | null
  saving: boolean
}>()
const emit = defineEmits<{
  (e: 'confirm'): void
  (e: 'close'): void
}>()

const { t, te } = useI18n()

const changes = computed(() => {
  if (!props.preview) return []
  const before = new Map(props.entry.view.members.map(m => [m.account_id, m.effective_share]))
  return props.preview.members
    .map(m => ({ id: m.account_id, name: m.name, before: before.get(m.account_id) ?? 0, after: m.effective_share }))
    .filter(change => Math.abs(change.after - change.before) >= 0.0005)
})

function warningText(warning: ShareWarning): string {
  const key = `admin.shareScheduling.preview.warnings.${warning.code}`
  if (typeof te === 'function' && !te(key)) return t('admin.shareScheduling.preview.warnings.unknown', { code: warning.code })
  return t(key, { id: warning.account_id ?? '', tier: warning.tier ?? '', detail: warning.detail ?? '' })
}
</script>
