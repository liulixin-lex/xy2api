<template>
  <div class="flex flex-wrap items-center gap-2" data-testid="support-alert-toggles">
    <button
      v-if="soundEnabled && soundSuspended"
      type="button"
      class="text-xs text-amber-700 hover:underline dark:text-amber-300"
      data-testid="support-alert-unlock"
      @click="unlockAudio"
    >{{ t('supportChat.alerts.unlockSound') }}</button>
    <button
      type="button"
      class="btn btn-sm"
      :class="soundEnabled ? 'btn-primary' : 'btn-secondary'"
      :aria-pressed="soundEnabled"
      data-testid="support-alert-sound"
      @click="toggleSound"
    >
      <Icon name="bell" size="sm" class="mr-1" />
      {{ soundEnabled ? t('supportChat.alerts.soundOn') : t('supportChat.alerts.soundOff') }}
    </button>
    <button
      v-if="notifySupported"
      type="button"
      class="btn btn-sm"
      :class="notifyEnabled ? 'btn-primary' : 'btn-secondary'"
      :aria-pressed="notifyEnabled"
      data-testid="support-alert-notify"
      @click="toggleNotify"
    >
      <Icon name="chatBubble" size="sm" class="mr-1" />
      {{ notifyEnabled ? t('supportChat.alerts.notifyOn') : t('supportChat.alerts.notifyOff') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useSupportAlertToggles } from './useSupportStaffAlerts'

const { t } = useI18n()
const { soundEnabled, notifyEnabled, soundSuspended, notifySupported, toggleSound, toggleNotify, unlockAudio } = useSupportAlertToggles()
</script>
