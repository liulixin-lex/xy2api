<template>
  <span :class="['sq-capsule sq-capsule--manual', { 'sq-capsule--all': allGroups }]" :data-sq-override="mode">
    <svg v-if="mode === 'exempt'" class="sq-capsule__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l7 3v5c0 4.4-2.9 8.3-7 10-4.1-1.7-7-5.6-7-10V6z" /></svg>
    <svg v-else class="sq-capsule__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4v11M7.5 10.5L12 15l4.5-4.5M5 20h14" /></svg>
    <template v-if="allGroups"><span class="sq-capsule__name">{{ t('admin.schedQuality.override.allGroups') }}</span>{{ ' ' }}</template>
    <span class="sq-capsule__state">{{ t(mode === 'exempt' ? 'admin.schedQuality.state.exempted' : 'admin.schedQuality.state.demoted') }}</span>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SchedOverrideMode } from '@/types/schedQuality'

// A manual override shown as a fact in the drawer, for every reader (the controls that change it need
// accounts:manage). Same look as the override capsules of the groups column (ui-proposal 4.1).
defineProps<{ mode: SchedOverrideMode; allGroups?: boolean }>()
const { t } = useI18n()
</script>
