<template>
  <button
    type="button"
    class="btn btn-secondary"
    data-test="group-category-manage"
    :title="t('admin.groupCategories.manage')"
    @click="openManager"
  >
    <Icon name="grid" size="md" class="mr-2" />
    {{ t('admin.groupCategories.manage') }}
  </button>

  <!-- 管理分类：改动先进草稿，点“保存”一次提交，“取消”全部放弃 -->
  <BaseDialog
    v-if="mounted"
    :show="open"
    :title="t('admin.groupCategories.manage')"
    width="normal"
    @close="closeManager"
  >
    <div class="space-y-4" data-test="group-category-manager">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.groupCategories.managerHint') }}</p>
      <p v-if="loadFailed" class="text-sm text-red-600 dark:text-red-400" role="alert">
        {{ t('admin.groupCategories.loadFailed') }}
      </p>
      <p v-else-if="listError" class="text-sm text-red-600 dark:text-red-400" role="alert">
        {{ listError }}
      </p>

      <VueDraggable v-model="draft" :animation="150" handle=".group-category-drag-handle" class="space-y-2">
        <div v-for="(row, index) in draft" :key="row.key" data-test="group-category-row">
          <div class="flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-2 py-1.5 dark:border-dark-600 dark:bg-dark-700">
            <span
              class="group-category-drag-handle flex shrink-0 cursor-grab items-center p-1 text-gray-400 hover:text-gray-600 active:cursor-grabbing dark:text-dark-400 dark:hover:text-dark-200"
              :title="t('admin.groupCategories.dragHandle')"
            >
              <Icon name="menu" size="sm" />
            </span>
            <input
              v-model="row.name"
              type="text"
              class="input min-w-0 flex-1 py-1.5"
              :maxlength="MAX_NAME_INPUT"
              :aria-invalid="rowError?.index === index"
              :aria-label="t('admin.groupCategories.namePlaceholder')"
              @input="clearErrors"
            />
            <span class="shrink-0 whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.groupCategories.groupCount', { count: row.groupCount }) }}
            </span>
            <button
              type="button"
              class="shrink-0 rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
              :title="t('admin.groupCategories.remove')"
              :aria-label="t('admin.groupCategories.remove')"
              data-test="group-category-remove"
              @click="removeRow(index)"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>
          <p v-if="rowError?.index === index" class="mt-1 text-xs text-red-600 dark:text-red-400" role="alert">
            {{ rowErrorText }}
          </p>
        </div>
      </VueDraggable>
      <p v-if="draft.length === 0" class="py-2 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.groupCategories.empty') }}
      </p>

      <form class="flex items-center gap-2" data-test="group-category-add" @submit.prevent="addRow">
        <input
          v-model="newName"
          type="text"
          class="input min-w-0 flex-1"
          :maxlength="MAX_NAME_INPUT"
          :placeholder="t('admin.groupCategories.namePlaceholder')"
          :aria-label="t('admin.groupCategories.namePlaceholder')"
        />
        <button type="submit" class="btn btn-secondary shrink-0" :disabled="!newName.trim()">
          <Icon name="plus" size="sm" class="mr-1" />
          {{ t('admin.groupCategories.add') }}
        </button>
      </form>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeManager">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          data-test="group-category-save"
          :disabled="saving || loading || loadFailed"
          @click="requestSave"
        >
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="confirmingDelete"
    :title="t('admin.groupCategories.deleteConfirmTitle')"
    :message="deleteConfirmMessage"
    :confirm-text="t('common.confirm')"
    :cancel-text="t('common.cancel')"
    :danger="true"
    @confirm="confirmDelete"
    @cancel="confirmingDelete = false"
  />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { useGroupCategoriesStore } from '@/stores/groupCategories'
import { useLazyMount } from '@/composables/useLazyMount'
import {
  MAX_GROUP_CATEGORY_NAME_LENGTH,
  groupCategoryErrorMessage,
  normalizeCategoryName,
  validateCategoryDraft,
  type CategoryDraftError
} from '@/utils/groupCategories'

interface DraftRow {
  key: string
  id?: number
  name: string
  groupCount: number
}

// 输入框留出余量，超长由校验提示而不是静默截断
const MAX_NAME_INPUT = MAX_GROUP_CATEGORY_NAME_LENGTH * 2

const { t } = useI18n()
const appStore = useAppStore()
const store = useGroupCategoriesStore()

const open = ref(false)
const mounted = useLazyMount(open)
const loading = ref(false)
const saving = ref(false)
const draft = ref<DraftRow[]>([])
const deletedIds = ref<number[]>([])
const newName = ref('')
const draftError = ref<CategoryDraftError | null>(null)
const confirmingDelete = ref(false)
let newRowSeq = 0

const loadFailed = computed(() => !loading.value && store.loadState === 'failed')
const rowError = computed(() => (draftError.value && draftError.value.index >= 0 ? draftError.value : null))
const errorText = (key: CategoryDraftError['key']) => {
  switch (key) {
    case 'nameRequired': return t('admin.groupCategories.nameRequired')
    case 'nameTooLong': return t('admin.groupCategories.nameTooLong')
    case 'nameInvalid': return t('admin.groupCategories.nameInvalid')
    case 'nameDuplicate': return t('admin.groupCategories.nameDuplicate')
    default: return t('admin.groupCategories.limitReached')
  }
}
const rowErrorText = computed(() => (rowError.value ? errorText(rowError.value.key) : ''))
const listError = computed(() => (draftError.value && draftError.value.index < 0 ? errorText(draftError.value.key) : ''))

// 删除的分类里还有分组时，保存前先确认
const deletedWithGroups = computed(() => {
  const deleted = new Set(deletedIds.value)
  return store.categories.filter((category) => deleted.has(category.id) && category.group_ids.length > 0)
})
const deleteConfirmMessage = computed(() => {
  const groups = new Set(deletedWithGroups.value.flatMap((category) => category.group_ids))
  return t('admin.groupCategories.deleteConfirm', {
    names: deletedWithGroups.value.map((category) => category.name).join('、'),
    count: groups.size
  })
})

const clearErrors = () => {
  draftError.value = null
}

async function openManager() {
  open.value = true
  draftError.value = null
  deletedIds.value = []
  newName.value = ''
  loading.value = true
  await store.reload()
  loading.value = false
  if (!open.value) return
  draft.value = store.categories.map((category) => ({
    key: `c${category.id}`,
    id: category.id,
    name: category.name,
    groupCount: category.group_ids.length
  }))
}

function closeManager() {
  if (saving.value) return
  open.value = false
  confirmingDelete.value = false
}

function addRow() {
  const name = normalizeCategoryName(newName.value)
  if (!name) return
  draft.value.push({ key: `n${++newRowSeq}`, name, groupCount: 0 })
  newName.value = ''
  clearErrors()
}

function removeRow(index: number) {
  const [row] = draft.value.splice(index, 1)
  if (row?.id) deletedIds.value.push(row.id)
  clearErrors()
}

function requestSave() {
  draftError.value = validateCategoryDraft(draft.value)
  if (draftError.value) return
  if (deletedWithGroups.value.length > 0) {
    confirmingDelete.value = true
    return
  }
  void submit()
}

function confirmDelete() {
  confirmingDelete.value = false
  void submit()
}

async function submit() {
  if (saving.value) return
  saving.value = true
  try {
    await store.save({
      items: draft.value.map((row) => {
        const name = normalizeCategoryName(row.name)
        return row.id ? { id: row.id, name } : { name }
      }),
      deleted_ids: [...deletedIds.value]
    })
    appStore.showSuccess(t('admin.groupCategories.saved'))
    saving.value = false
    closeManager()
  } catch (error) {
    // 服务端拒绝时保留草稿，管理员改完再保存
    appStore.showError(groupCategoryErrorMessage(error, t, t('admin.groupCategories.saveFailed')))
  } finally {
    saving.value = false
  }
}
</script>
