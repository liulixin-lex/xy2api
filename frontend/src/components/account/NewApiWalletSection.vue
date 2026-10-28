<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="newapi-wallet-section">
    <button
      type="button"
      class="flex items-center gap-2 text-sm text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
      data-testid="newapi-wallet-toggle"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      <svg
        :class="['h-4 w-4 transition-transform', expanded ? 'rotate-90' : '']"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
      >
        <path stroke-linecap="round" stroke-linejoin="round" d="M9 5l7 7-7 7" />
      </svg>
      <span>{{ t('admin.accounts.newapiWallet.title') }}</span>
      <!-- 折叠时在标题旁显示状态：已填的令牌、待清除或无效的用户 ID 在保存时仍会生效，不能被折叠藏起来。 -->
      <span
        v-if="!expanded && collapsedStatus"
        :class="['inline-flex items-center rounded px-2 py-0.5 text-xs font-medium', collapsedStatus.className]"
        data-testid="newapi-wallet-collapsed-status"
      >
        {{ collapsedStatus.label }}
      </span>
    </button>

    <div v-if="expanded" class="mt-3 space-y-3">
      <div>
        <label class="input-label">{{ t('admin.accounts.newapiWallet.accessToken') }}</label>
        <input
          type="password"
          class="input font-mono"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore="true"
          data-testid="newapi-wallet-token"
          :value="model.accessToken"
          :disabled="model.clearToken"
          :placeholder="configured ? t('admin.accounts.newapiWallet.tokenKeepPlaceholder') : ''"
          @input="onTokenInput"
        />
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.newapiWallet.userId') }}</label>
        <input
          type="text"
          inputmode="numeric"
          maxlength="20"
          autocomplete="off"
          class="input font-mono"
          data-testid="newapi-wallet-user-id"
          :value="model.userId"
          :aria-invalid="userIdInvalid"
          @input="onUserIdInput"
        />
        <p v-if="userIdInvalid" class="input-error-text" data-testid="newapi-wallet-user-id-error">
          {{ t('admin.accounts.newapiWallet.userIdInvalid') }}
        </p>
      </div>

      <div v-if="configured" class="flex items-center gap-3">
        <span
          v-if="model.clearToken"
          class="inline-flex items-center rounded bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
          data-testid="newapi-wallet-clear-pending"
        >
          {{ t('admin.accounts.newapiWallet.clearPending') }}
        </span>
        <span
          v-else
          class="inline-flex items-center rounded bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
          data-testid="newapi-wallet-configured"
        >
          {{ t('admin.accounts.newapiWallet.configured') }}
        </span>
        <button
          type="button"
          class="text-sm text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          data-testid="newapi-wallet-clear"
          @click="toggleClear"
        >
          {{ model.clearToken ? t('admin.accounts.newapiWallet.undoClear') : t('admin.accounts.newapiWallet.clear') }}
        </button>
      </div>

      <p class="input-hint">{{ t('admin.accounts.newapiWallet.hint') }}</p>
      <p v-if="configured" class="input-hint" data-testid="newapi-wallet-host-change-hint">
        {{ t('admin.accounts.newapiWallet.hostChangeHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
// 「new-api 钱包余额（可选）」小节（xy-upstream-balance）：放在 API Key 输入框之后，默认折叠。
// 令牌只存在于本组件的 v-model 草稿里，不打日志、不放进 title/tooltip、不落本地存储。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isNewApiWalletUserIdValid, type NewApiWalletDraft } from './newApiWalletCredentials'

const props = withDefaults(defineProps<{ configured?: boolean }>(), { configured: false })
const model = defineModel<NewApiWalletDraft>({ required: true })

const { t } = useI18n()

const hasContent = () => props.configured || model.value.userId !== '' || model.value.accessToken !== ''
const expanded = ref(hasContent())
// 编辑弹窗可能在挂载后才回填账号数据：一旦有内容就展开，不会自动收起。
watch(hasContent, (value) => {
  if (value) expanded.value = true
})

// 已存的用户 ID 不合法（例如 u42 或超过 20 位）时就地提示；提交会被弹窗拦下。
const userIdInvalid = computed(() => !isNewApiWalletUserIdValid(model.value.userId))

const TAG_EMERALD = 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
const TAG_AMBER = 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
const TAG_RED = 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'

const collapsedStatus = computed(() => {
  if (userIdInvalid.value) return { label: t('admin.accounts.newapiWallet.userIdInvalid'), className: TAG_RED }
  if (props.configured && model.value.clearToken) {
    return { label: t('admin.accounts.newapiWallet.clearPending'), className: TAG_AMBER }
  }
  if (model.value.accessToken.trim() !== '') return { label: t('admin.accounts.newapiWallet.tokenFilled'), className: TAG_AMBER }
  if (props.configured) return { label: t('admin.accounts.newapiWallet.configured'), className: TAG_EMERALD }
  return null
})

function onTokenInput(event: Event) {
  model.value = { ...model.value, accessToken: (event.target as HTMLInputElement).value }
}

function onUserIdInput(event: Event) {
  const target = event.target as HTMLInputElement
  const digits = target.value.replace(/\D+/g, '')
  if (target.value !== digits) target.value = digits
  model.value = { ...model.value, userId: digits }
}

function toggleClear() {
  model.value = model.value.clearToken
    ? { ...model.value, clearToken: false }
    : { ...model.value, clearToken: true, accessToken: '' }
}
</script>
