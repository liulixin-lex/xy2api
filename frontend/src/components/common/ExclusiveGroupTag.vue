<template>
  <!-- 用户端的专属分组标签；是否显示由调用方按 is_exclusive 与 show_exclusive_badge 决定 -->
  <span
    data-test="exclusive-group-tag"
    class="inline-flex shrink-0 items-center gap-1 rounded-md bg-purple-50 px-1.5 py-0.5 text-[11px] font-medium text-purple-600 dark:bg-purple-900/20 dark:text-purple-400"
    :title="t('availableChannels.exclusiveTooltip')"
  >
    <Icon name="shield" size="xs" class="h-3 w-3" />
    {{ t('availableChannels.exclusive') }}
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
</script>
