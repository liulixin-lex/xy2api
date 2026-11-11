<template>
  <div class="flex flex-wrap items-center gap-2">
    <button
      v-if="campaign.status === 'sending'"
      type="button"
      class="btn btn-secondary btn-sm"
      :disabled="busy"
      data-testid="email-campaign-pause"
      @click="emit('pause', campaign)"
    >
      {{ t('admin.emailCampaigns.actions.pause') }}
    </button>
    <button
      v-if="campaign.status === 'paused'"
      type="button"
      class="btn btn-secondary btn-sm"
      :disabled="busy"
      data-testid="email-campaign-resume"
      @click="emit('resume', campaign)"
    >
      {{ t('admin.emailCampaigns.actions.resume') }}
    </button>
    <button
      v-if="isActiveStatus(campaign.status)"
      type="button"
      class="btn btn-secondary btn-sm text-red-600 dark:text-red-400"
      :disabled="busy"
      data-testid="email-campaign-cancel"
      @click="emit('cancel', campaign)"
    >
      {{ t('admin.emailCampaigns.actions.cancel') }}
    </button>
    <slot />
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { isActiveStatus } from './campaignFormat'
import type { Campaign } from './types'

defineProps<{ campaign: Campaign; busy?: boolean }>()
const emit = defineEmits<{
  (e: 'pause', campaign: Campaign): void
  (e: 'resume', campaign: Campaign): void
  (e: 'cancel', campaign: Campaign): void
}>()

const { t } = useI18n()
</script>
