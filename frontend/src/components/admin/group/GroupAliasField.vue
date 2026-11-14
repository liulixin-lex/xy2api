<template>
  <div data-test="group-alias-field">
    <label class="input-label" :for="id">
      {{ t('admin.groups.alias.label') }}<span class="ml-1 font-normal text-fg-secondary">{{ t('admin.groups.alias.optional') }}</span>
    </label>
    <div class="flex flex-wrap items-center gap-3">
      <input
        :id="id"
        v-model="alias"
        type="text"
        class="input w-28 flex-none"
        :class="{ 'input-error': error }"
        autocomplete="off"
        :aria-invalid="error ? 'true' : undefined"
        :aria-describedby="`${id}-note`"
        data-test="group-alias-input"
      />
      <div class="flex items-center gap-2" aria-live="polite">
        <span class="text-xs text-fg-secondary">{{ t('admin.groups.alias.preview') }}</span>
        <GroupAliasPill :group="{ name, alias }" tone="ok" data-test="group-alias-preview" />
        <span v-if="!alias.trim()" class="text-xs text-fg-secondary">{{ t('admin.groups.alias.auto') }}</span>
      </div>
    </div>
    <p v-if="error" :id="`${id}-note`" class="input-error-text" data-test="group-alias-error">{{ error }}</p>
    <p v-else :id="`${id}-note`" class="input-hint">{{ t('admin.groups.alias.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * The alias input of the group forms (spec 6): a live preview with the accounts page's group pill, the automatic text
 * while it is empty, and the length rule (utils/groupAlias) as an inline error. The form checks groupAliasFits()
 * before it submits; a backend rejection (INVALID_GROUP_ALIAS) is shown here through showServerError() until the
 * alias changes.
 */
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import { groupAliasFits } from '@/utils/groupAlias'

defineProps<{ name: string }>()
const alias = defineModel<string>({ required: true })
const { t } = useI18n()
const id = `group-alias-${useId()}`

const serverError = ref('')
watch(alias, () => {
  serverError.value = ''
})
const error = computed(() => serverError.value || (groupAliasFits(alias.value) ? '' : t('admin.groups.alias.tooLong')))

defineExpose({
  showServerError: () => {
    serverError.value = t('admin.groups.alias.invalid')
  }
})
</script>
