<template>
  <div class="grid min-w-0 w-full grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap sm:gap-3">
    <!-- XY2API：筛选项全部平铺，不折叠到“更多筛选”（2026-10-19 用户要求）；手机上两列排布。 -->
    <SearchInput
      :model-value="searchQuery"
      :placeholder="t('admin.accounts.searchAccounts')"
      class="col-span-2 min-w-0 w-full sm:w-72"
      @update:model-value="$emit('update:searchQuery', $event)"
      @search="$emit('change')"
    />
    <Select :model-value="filters.platform" :aria-label="t('admin.accounts.allPlatforms')" class="min-w-0 w-full sm:w-44" :options="pOpts" @update:model-value="updatePlatform" @change="$emit('change')" />
    <Select :model-value="filters.type" :aria-label="t('admin.accounts.allTypes')" class="min-w-0 w-full sm:w-44" :options="tOpts" @update:model-value="updateType" @change="$emit('change')" />
    <Select :model-value="filters.status" :aria-label="t('admin.accounts.allStatus')" class="min-w-0 w-full sm:w-44" :options="sOpts" @update:model-value="updateStatus" @change="$emit('change')" />
    <Select :model-value="filters.privacy_mode" :aria-label="t('admin.accounts.allPrivacyModes')" class="min-w-0 w-full sm:w-44" :options="privacyOpts" @update:model-value="updatePrivacyMode" @change="$emit('change')" />
    <Select :model-value="filters.iq_status" :aria-label="t('admin.accounts.allIQStatuses')" class="min-w-0 w-full sm:w-44" :options="iqOpts" @update:model-value="updateIQStatus" @change="$emit('change')" />
    <GroupCategoryFilter :model-value="filters.group_category ?? ''" class="min-w-0 w-full sm:w-44" @update:model-value="(value: string) => $emit('update:filters', { ...filters, group_category: value })" @change="$emit('change')" />
    <Select :model-value="filters.group" :aria-label="t('admin.accounts.allGroups')" class="min-w-0 w-full sm:w-44" :options="gOpts" @update:model-value="updateGroup" @change="$emit('change')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'; import { useI18n } from 'vue-i18n'; import Select from '@/components/common/Select.vue'; import SearchInput from '@/components/common/SearchInput.vue'
import type { AdminGroup } from '@/types'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import GroupCategoryFilter from '@/components/admin/group/GroupCategoryFilter.vue'
const props = defineProps<{ searchQuery: string; filters: Record<string, any>; groups?: AdminGroup[] }>()
const emit = defineEmits(['update:searchQuery', 'update:filters', 'change']); const { t } = useI18n()
const updatePlatform = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, platform: value }) }
const updateType = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, type: value }) }
const updateStatus = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, status: value }) }
const updatePrivacyMode = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, privacy_mode: value }) }
const updateIQStatus = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, iq_status: value }) }
const updateGroup = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, group: value }) }
const pOpts = computed(() => [{ value: '', label: t('admin.accounts.allPlatforms') }, ...CONCRETE_PLATFORM_OPTIONS])
const tOpts = computed(() => [{ value: '', label: t('admin.accounts.allTypes') }, { value: 'oauth', label: t('admin.accounts.oauthType') }, { value: 'setup-token', label: t('admin.accounts.setupToken') }, { value: 'apikey', label: t('admin.accounts.apiKey') }, { value: 'bedrock', label: 'AWS Bedrock' }])
const sOpts = computed(() => [{ value: '', label: t('admin.accounts.allStatus') }, { value: 'active', label: t('admin.accounts.status.active') }, { value: 'inactive', label: t('admin.accounts.status.inactive') }, { value: 'error', label: t('admin.accounts.status.error') }, { value: 'rate_limited', label: t('admin.accounts.status.rateLimited') }, { value: 'temp_unschedulable', label: t('admin.accounts.status.tempUnschedulable') }, { value: 'unschedulable', label: t('admin.accounts.status.unschedulable') }])
const privacyOpts = computed(() => [
  { value: '', label: t('admin.accounts.allPrivacyModes') },
  { value: '__unset__', label: t('admin.accounts.privacyUnset') },
  { value: 'training_off', label: 'Privacy' },
  { value: 'training_set_cf_blocked', label: 'CF' },
  { value: 'training_set_failed', label: 'Fail' }
])
const iqOpts = computed(() => [
  { value: '', label: t('admin.accounts.allIQStatuses') },
  { value: 'smart', label: t('admin.accounts.iqCheckStatus.smart') },
  { value: 'degraded', label: t('admin.accounts.iqCheckStatus.degraded') },
  { value: 'unknown', label: t('admin.accounts.iqCheckStatus.unknown') }
])
const gOpts = computed(() => [
  { value: '', label: t('admin.accounts.allGroups') },
  { value: 'ungrouped', label: t('admin.accounts.ungroupedGroup') },
  ...(props.groups || []).map(g => ({ value: String(g.id), label: g.name }))
])
</script>
