<template>
  <div class="account-name" :title="title">
    <a
      v-if="homepage"
      :href="homepage"
      target="_blank"
      rel="noopener noreferrer"
      class="account-name__main account-name__link"
      data-test="account-name"
    >{{ account.name }}</a>
    <span v-else class="account-name__main" data-test="account-name">{{ account.name }}</span>
    <span class="account-name__sub">{{ t('admin.accounts.table.nameSub', { platform: platformLabel(account.platform), type: typeLabel }) }}</span>
  </div>
</template>

<script setup lang="ts">
/** Name column (prototype): the account name, and "platform · type" below it. Email and parent show on hover. */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { platformLabel } from '@/utils/platformColors'
import { sanitizeUrl } from '@/utils/url'
import { accountTypeLabel } from './accountLabels'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()

const typeLabel = computed(() => accountTypeLabel(props.account.type, t))

/** An API key account with a safe base URL links its name to the site's home page. */
const homepage = computed(() => {
  const account = props.account
  if (account.type !== 'apikey' || typeof account.credentials?.base_url !== 'string') return ''
  const baseUrl = sanitizeUrl(account.credentials.base_url)
  return baseUrl ? new URL(baseUrl).origin : ''
})

const email = computed(() => {
  const a = props.account
  return a.extra?.email_address || a.extra?.email || a.credentials?.email || a.parent_email || ''
})

const title = computed(() =>
  [props.account.name, email.value, props.account.parent_chatgpt_account_id, homepage.value].filter(Boolean).join('\n')
)
</script>

<style scoped>
.account-name {
  display: flex;
  min-width: 0;
  flex-direction: column;
}
.account-name__main {
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: var(--font-size-name);
  font-weight: var(--font-weight-medium);
  line-height: var(--line-height-name);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.account-name__link {
  text-decoration: underline dotted 1px;
  text-decoration-color: var(--color-line-control);
  text-underline-offset: 3px;
}
.account-name__sub {
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
