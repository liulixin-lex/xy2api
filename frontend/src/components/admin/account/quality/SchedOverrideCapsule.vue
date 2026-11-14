<template>
  <span class="sq-capsule sq-capsule--manual" data-sq-override="exempt">
    <svg class="sq-capsule__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l7 3v5c0 4.4-2.9 8.3-7 10-4.1-1.7-7-5.6-7-10V6z" /></svg>
    <template v-if="allGroups"><span class="sq-capsule__name">{{ t('admin.schedQuality.override.allGroups') }}</span>{{ ' ' }}</template>
    <span class="sq-capsule__state">{{ t('admin.schedQuality.state.exempted') }}</span>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

// An exemption shown as a fact in the drawer, for every reader (the controls that change it need
// accounts:manage). Same look as the override capsules of the groups column (ui-proposal 4.1).
defineProps<{ allGroups?: boolean }>()
const { t } = useI18n()
</script>
