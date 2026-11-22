<template>
  <ul class="mt-2 max-w-prose list-disc space-y-1 pl-5 text-xs text-gray-600 dark:text-gray-300" data-testid="collaboration-plaintext-consent">
    <li>{{ t('admin.resilience.settings.collab.consent.visible') }}</li>
    <li>{{ t('admin.resilience.settings.collab.consent.moderation') }}</li>
    <li>{{ t('admin.resilience.settings.collab.consent.permanent') }}</li>
  </ul>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>
