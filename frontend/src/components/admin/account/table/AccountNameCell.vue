<template>
  <div class="account-name" v-tip="title" @click="reveal">
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
/**
 * Name column (prototype): the account name, and "platform · type" below it, each cut with "…" at the column's width
 * (on a phone the frozen identity column, mobile list plan). The whole name, email, parent and home page show in the
 * tip on hover, or at once on a tap or click (touch has no hover). On a touch screen the first tap on a linked name
 * shows the tip, the home page in it; a second tap, with the tip showing, opens the site.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { platformLabel } from '@/utils/platformColors'
import { sanitizeUrl } from '@/utils/url'
import { usePointerCoarse } from '@/composables/usePointerCoarse'
import { isTipShowing, showTipNow, vTip } from '@/utils/tip'
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
const coarse = usePointerCoarse()
/** A tap or click shows the tip at once. A mouse click on the link opens the site; a tap does once the tip shows. */
const reveal = (event: MouseEvent) => {
  const onLink = event.target instanceof Element && !!event.target.closest('a')
  if (onLink && (!coarse.value || isTipShowing(event.currentTarget))) return
  if (onLink) event.preventDefault()
  showTipNow(event.currentTarget)
}
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
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-name);
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* A link without an underline (prototype); hovering shows one (7i-8). */
.account-name__link {
  text-decoration: none;
  text-underline-offset: 3px;
}
@media (hover: hover) {
  .account-name__link:hover {
    text-decoration: underline;
  }
}
.account-name__sub {
  overflow: hidden;
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
