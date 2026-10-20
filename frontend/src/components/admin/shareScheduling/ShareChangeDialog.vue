<template>
  <BaseDialog :show="pending !== null" :title="title" :width="kind === 'share' ? 'wide' : 'narrow'" @close="cancel">
    <div v-if="pending" class="space-y-4" data-testid="share-change">
      <p class="text-sm text-gray-700 dark:text-dark-200">{{ body }}</p>
      <p v-if="pending.enableGlobal" class="rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="share-change-enable-global">
        {{ t('admin.shareScheduling.change.enableGlobal') }}
      </p>

      <template v-if="kind === 'share'">
        <p v-if="store.previewing" class="text-sm text-gray-500 dark:text-dark-400" role="status">{{ t('admin.shareScheduling.change.previewing') }}</p>
        <p v-else-if="pending.previewError" class="text-sm text-red-700 dark:text-red-300" role="alert">{{ t('admin.shareScheduling.change.previewFailed', { error: pending.previewError }) }}</p>
        <template v-else-if="pending.preview">
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.change.expected') }}</h4>
            <div class="mt-2 max-h-72 overflow-auto">
              <table class="w-full text-left text-sm" data-testid="share-change-members">
                <thead>
                  <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400">
                    <th class="p-2">{{ t('admin.shareScheduling.change.columns.account') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.change.columns.priority') }}</th>
                    <th class="p-2 text-right">{{ t('admin.shareScheduling.change.columns.loadFactor') }}</th>
                    <th class="w-2/5 p-2">{{ t('admin.shareScheduling.change.columns.share') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="member in receiving" :key="member.account_id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                    <td class="p-2">
                      <span class="font-medium text-gray-900 dark:text-white">{{ member.name }}</span>
                      <span class="ml-1 text-xs text-gray-500 dark:text-dark-400">#{{ member.account_id }}</span>
                    </td>
                    <td class="p-2 text-right tabular-nums">{{ member.priority }}</td>
                    <td class="p-2 text-right tabular-nums">{{ member.effective_load_factor }}</td>
                    <td class="p-2">
                      <ShareShareBar :value="member.expected_share" />
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-if="idleCount" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.change.others', { count: idleCount }) }}</p>
          </div>
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.change.risks') }}</h4>
            <ul v-if="warnings.length" class="mt-2 space-y-1 text-sm" data-testid="share-change-risks">
              <li
                v-for="(warning, index) in warnings"
                :key="index"
                class="rounded-lg px-3 py-2"
                :class="warning.severity === 'warn' ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200' : 'bg-gray-50 text-gray-700 dark:bg-dark-700/60 dark:text-dark-200'"
                :data-testid="'share-change-risk-' + warning.code"
              >{{ labels.risk(warning) }}</li>
            </ul>
            <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.change.noRisks') }}</p>
          </div>
        </template>
      </template>

      <p v-if="store.actionError" class="text-sm text-red-700 dark:text-red-300" role="alert">{{ t('admin.shareScheduling.change.failed', { error: store.actionError }) }}</p>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="store.saving" @click="cancel">{{ t('admin.shareScheduling.change.cancel') }}</button>
        <button
          type="button"
          class="btn"
          :class="kind === 'sub2api' || kind === 'keepOff' ? 'btn-danger' : 'btn-primary'"
          :disabled="store.saving || store.previewing"
          data-testid="share-change-confirm"
          @click="store.confirmChange()"
        >{{ store.saving ? t('admin.shareScheduling.change.saving') : t('admin.shareScheduling.change.confirm') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ShareShareBar from './ShareShareBar.vue'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { namedWarnings } from '@/utils/shareAttention'

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const store = useShareSchedulingStore()

const pending = computed(() => store.pending)

/** What the pending change does: switch to share, go back, or flip session keeping. */
const kind = computed<'share' | 'sub2api' | 'keepOn' | 'keepOff'>(() => {
  const change = store.pending
  const current = store.view
  if (!change || !current) return 'share'
  if (change.update.scheduler !== current.scheduler) return change.update.scheduler === 'share' ? 'share' : 'sub2api'
  return change.update.session_keep ? 'keepOn' : 'keepOff'
})

const title = computed(() => ({
  share: t('admin.shareScheduling.change.titleShare'),
  sub2api: t('admin.shareScheduling.change.titleSub2api'),
  keepOn: t('admin.shareScheduling.change.titleKeepOn'),
  keepOff: t('admin.shareScheduling.change.titleKeepOff')
})[kind.value])

const body = computed(() => ({
  share: t('admin.shareScheduling.change.shareBody'),
  sub2api: t('admin.shareScheduling.change.sub2apiBody'),
  keepOn: t('admin.shareScheduling.change.keepOnBody'),
  keepOff: t('admin.shareScheduling.change.keepOffBody')
})[kind.value])

const names = computed(() => new Map((store.view?.members ?? []).map(m => [m.account_id, m.name])))
const receiving = computed(() => (store.pending?.preview?.members ?? [])
  .filter(m => m.expected_share > 0)
  .sort((a, b) => b.expected_share - a.expected_share || a.account_id - b.account_id))
const idleCount = computed(() => (store.pending?.preview?.members.length ?? 0) - receiving.value.length)
const warnings = computed(() => namedWarnings(store.pending?.preview?.warnings ?? [], names.value)
  .filter(w => w.code !== 'global_switch_off' || !store.pending?.enableGlobal))

function cancel() {
  if (!store.saving) store.cancelChange()
}
</script>
