<template>
  <section class="card p-5" data-testid="share-attention">
    <h3 class="font-semibold text-gray-900 dark:text-white">
      {{ t('admin.shareScheduling.attention.title') }}
      <span v-if="items.length" class="ml-1 text-sm font-normal text-gray-500 dark:text-dark-400">({{ items.length }})</span>
    </h3>
    <ul v-if="items.length" class="mt-3 space-y-2">
      <li
        v-for="(item, index) in items"
        :key="index"
        class="flex items-start gap-2 rounded-lg px-3 py-2 text-sm"
        :class="item.severity === 'warn' ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200' : 'bg-gray-50 text-gray-700 dark:bg-dark-700/60 dark:text-dark-200'"
        :data-testid="'share-attention-' + item.code"
      >
        <span class="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full" :class="item.severity === 'warn' ? 'bg-amber-500' : 'bg-gray-400'" aria-hidden="true" />
        <span class="min-w-0">{{ labels.risk(item) }}</span>
      </li>
    </ul>
    <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.attention.none') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SharePolicyView } from '@/api/admin/shareScheduling'
import { useShareSchedulingLabels } from '@/composables/useShareSchedulingLabels'
import { shareAttention } from '@/utils/shareAttention'

const props = defineProps<{
  view: SharePolicyView
  globalEnabled: boolean | null
}>()

const { t } = useI18n()
const labels = useShareSchedulingLabels()
const items = computed(() => shareAttention(props.view, props.globalEnabled))
</script>
