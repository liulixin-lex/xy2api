<template>
  <!-- 账号列表的“编辑分组”弹窗：只改 group_ids，取消不保存 -->
  <BaseDialog
    :show="true"
    :title="t('admin.groupCategories.editAccountGroups', { name: account.name })"
    width="normal"
    @close="handleClose"
  >
    <div data-test="account-groups-dialog">
      <div v-if="!fresh" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('common.loading') }}
      </div>
      <GroupSelector
        v-else
        v-model="selected"
        :groups="selectableGroups"
        :platform="fresh.platform"
        :mixed-scheduling="fresh.extra?.mixed_scheduling === true"
        category-tabs
        list-class="!max-h-80"
      />
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          data-test="account-groups-confirm"
          :disabled="saving || !fresh"
          @click="submit()"
        >
          {{ saving ? t('common.saving') : t('common.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="mixedChannelMessage !== null"
    :title="t('admin.accounts.mixedChannelWarningTitle')"
    :message="mixedChannelMessage ?? ''"
    :confirm-text="t('common.confirm')"
    :cancel-text="t('common.cancel')"
    :danger="true"
    @confirm="confirmMixedChannel"
    @cancel="mixedChannelMessage = null"
  />
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import { useAppStore } from '@/stores/app'
import { mergeSelectableGroups, sameIdList } from '@/utils/accountGroupSelection'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account, AccountListItem, AdminGroup, UpdateAccountRequest } from '@/types'

const props = defineProps<{
  account: AccountListItem
  groups: AdminGroup[]
}>()

const emit = defineEmits<{
  close: []
  updated: [account: Account]
}>()

const { t } = useI18n()
const appStore = useAppStore()

const fresh = ref<Account | null>(null)
const selected = ref<number[]>([])
const saving = ref(false)
const mixedChannelMessage = ref<string | null>(null)
let initialIds: number[] = []
let requestSeq = 0

// 已绑定但停用的分组也列出来并保持勾选，和编辑账号弹窗一致
const selectableGroups = computed(() => mergeSelectableGroups(props.groups, fresh.value))

// 打开时按 id 取一次最新账号：列表行里没有停用分组，分组顺序也以详情为准
async function load(accountId: number) {
  const seq = ++requestSeq
  fresh.value = null
  try {
    const account = await adminAPI.accounts.getById(accountId)
    if (seq !== requestSeq) return
    initialIds = [...(account.group_ids ?? [])]
    selected.value = [...initialIds]
    fresh.value = account
  } catch (error) {
    if (seq !== requestSeq) return
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
    emit('close')
  }
}

watch(() => props.account.id, (id) => void load(id), { immediate: true })

function handleClose() {
  if (saving.value) return
  emit('close')
}

/** 新勾选的分组追加在末尾，原有顺序不变（GroupSelector 本身就这样维护）。 */
async function submit(confirmMixedChannel = false) {
  const account = fresh.value
  if (!account || saving.value) return
  const groupIds = [...selected.value]
  if (!confirmMixedChannel && sameIdList(groupIds, initialIds)) {
    emit('close')
    return
  }
  const payload: UpdateAccountRequest = confirmMixedChannel
    ? { group_ids: groupIds, confirm_mixed_channel_risk: true }
    : { group_ids: groupIds }
  saving.value = true
  try {
    const updated = await adminAPI.accounts.update(account.id, payload)
    appStore.showSuccess(t('admin.accounts.accountUpdated'))
    saving.value = false
    emit('updated', updated)
    emit('close')
  } catch (error) {
    // 混合渠道 409 时后端还没写入，确认后带上标记重发
    const apiError = error as { status?: number; error?: string; message?: string }
    if (apiError?.status === 409 && apiError.error === 'mixed_channel_warning') {
      mixedChannelMessage.value = apiError.message || t('admin.accounts.failedToUpdate')
      return
    }
    appStore.showError(extractApiErrorMessage(error, t('admin.accounts.failedToUpdate')))
  } finally {
    saving.value = false
  }
}

function confirmMixedChannel() {
  mixedChannelMessage.value = null
  void submit(true)
}
</script>
