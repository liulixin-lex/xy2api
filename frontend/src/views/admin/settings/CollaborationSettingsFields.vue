<template>
  <div class="rounded-xl border border-gray-200 p-4 lg:col-span-2 dark:border-dark-600" data-testid="resilience-card-collab">
    <h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.resilience.settings.collab.title') }}</h3>
    <p class="mt-1 max-w-prose text-xs text-gray-500 dark:text-dark-400">{{ t('admin.resilience.settings.collab.hint') }}</p>
    <p v-if="unsupported" role="status" class="mt-3 text-sm text-amber-700 dark:text-amber-300" data-testid="resilience-collab-unsupported">{{ t('admin.resilience.settings.collab.unsupported') }}</p>
    <ul class="mt-4 space-y-4">
      <li v-for="key in COLLAB_BOOLEAN_FIELDS" :key="key" class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <p class="text-sm text-gray-900 dark:text-white">{{ t(`admin.resilience.settings.collab.${key}`) }}</p>
          <p :id="`collab-${key}-hint`" class="mt-1 max-w-prose text-xs text-gray-500 dark:text-dark-400">{{ t(`admin.resilience.settings.collab.${key}Hint`) }}</p>
        </div>
        <Toggle
          :model-value="modelValue[key]"
          :disabled="disabled || ((key === 'record_routing' || key === 'lossless_first') && !modelValue.ladder)"
          :aria-label="t(`admin.resilience.settings.collab.${key}`)"
          :aria-describedby="`collab-${key}-hint`"
          :data-testid="`resilience-collab-${key}`"
          @update:model-value="update({ [key]: $event })"
        />
      </li>
    </ul>
    <fieldset class="mt-4 border-t border-gray-100 pt-4 dark:border-dark-700" :disabled="disabled || !modelValue.ladder" aria-describedby="collab-local-ending-hint">
      <legend class="input-label">{{ t('admin.resilience.settings.collab.local_ending') }}</legend>
      <p id="collab-local-ending-hint" class="mb-3 max-w-prose text-xs text-gray-500 dark:text-dark-400">{{ t('admin.resilience.settings.collab.local_endingHint') }}</p>
      <div class="flex flex-wrap gap-4">
        <label v-for="mode in COLLAB_LOCAL_ENDING_MODES" :key="mode" class="flex items-center gap-2 text-sm text-gray-900 dark:text-white">
          <input
            type="radio"
            name="collab-local-ending"
            :value="mode"
            :checked="modelValue.local_ending.mode === mode"
            class="h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-500"
            :data-testid="`resilience-collab-local-ending-${mode}`"
            @change="update({ local_ending: { ...modelValue.local_ending, mode } })"
          >
          {{ t(`admin.resilience.settings.collab.modes.${mode}`) }}
        </label>
      </div>
      <div v-if="modelValue.local_ending.mode === 'groups'" class="mt-3">
        <p v-if="groupsLoading" role="status" class="text-sm text-gray-500 dark:text-dark-400">{{ t('common.loading') }}</p>
        <div v-else-if="groupsFailed" role="alert" class="flex flex-wrap items-center gap-3">
          <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.resilience.settings.collab.groupsFailed') }}</p>
          <button type="button" class="btn btn-secondary" @click="loadGroups">{{ t('admin.resilience.settings.reload') }}</button>
        </div>
        <GroupSelector
          v-else-if="groupsLoaded"
          :model-value="modelValue.local_ending.groups"
          :groups="groups"
          preserve-unavailable
          data-testid="resilience-collab-groups"
          @update:model-value="update({ local_ending: { ...modelValue.local_ending, groups: $event } })"
        />
        <p v-if="!modelValue.local_ending.groups.length" class="input-hint">{{ t('admin.resilience.settings.collab.groupsEmpty') }}</p>
      </div>
    </fieldset>
    <p v-if="!modelValue.ladder" class="mt-3 text-xs text-gray-500 dark:text-dark-400" data-testid="resilience-collab-dependency">{{ t('admin.resilience.settings.collab.ladderRequired') }}</p>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CollaborationSettings } from '@/api/admin/resilience'
import type { AdminGroup } from '@/types'
import Toggle from '@/components/common/Toggle.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import { COLLAB_BOOLEAN_FIELDS, COLLAB_LOCAL_ENDING_MODES } from '@/utils/collaborationSettings'

const props = defineProps<{ modelValue: CollaborationSettings; disabled: boolean; unsupported?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: CollaborationSettings] }>()
const { t } = useI18n()
const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false), groupsLoaded = ref(false), groupsFailed = ref(false)
let disposed = false

function update(patch: Partial<CollaborationSettings>) {
  emit('update:modelValue', { ...props.modelValue, ...patch })
}

async function loadGroups() {
  if (groupsLoading.value) return
  groupsLoading.value = true; groupsFailed.value = false
  try {
    const result = await adminAPI.groups.getAllIncludingInactive()
    if (disposed) return
    groups.value = result
    groupsLoaded.value = true
  } catch {
    if (!disposed) groupsFailed.value = true
  } finally {
    if (!disposed) groupsLoading.value = false
  }
}

watch(() => props.modelValue.local_ending.mode, (mode) => {
  if (mode === 'groups' && !groupsLoaded.value) void loadGroups()
}, { immediate: true })
onBeforeUnmount(() => { disposed = true })
</script>
