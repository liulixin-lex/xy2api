<template>
  <section class="card space-y-4 p-6" aria-labelledby="management-mcp-title" :aria-busy="loading || saving">
    <div>
      <h2 id="management-mcp-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('managementMcp.adminTitle') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('managementMcp.adminDescription') }}</p>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <fieldset v-else :disabled="!loaded || saving" class="space-y-4">
      <label class="flex items-center gap-2">
        <input v-model="draft.enabled" data-test="enabled" type="checkbox" />
        {{ t('managementMcp.adminEnabled') }}
      </label>
      <label class="block text-sm font-medium">
        {{ t('managementMcp.endpoint') }}
        <input v-model="draft.public_url" data-test="public-url" class="input mt-1 font-mono" type="url" placeholder="https://api.example.com/api/v1/mcp" aria-describedby="management-mcp-url-hint" />
      </label>
      <p id="management-mcp-url-hint" class="text-sm text-gray-500 dark:text-gray-400">{{ t('managementMcp.adminURLHint') }}</p>
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('managementMcp.adminDisableHint') }}</p>
      <div class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
        <h3 class="font-medium">{{ t('managementMcp.roleAccessTitle') }}</h3>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('managementMcp.roleAccessHint') }}</p>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
            <h4 class="font-medium">{{ t('managementMcp.operators') }}</h4>
            <label class="flex items-center gap-2"><input v-model="draft.allow_operators" data-test="allow-operators" type="checkbox" />{{ t('managementMcp.allowAccess') }}</label>
            <label class="flex items-center gap-2"><input v-model="draft.show_operator_menu" data-test="operator-menu" type="checkbox" />{{ t('managementMcp.showMenu') }}</label>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('managementMcp.operatorAccessHint') }}</p>
          </div>
          <div class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
            <h4 class="font-medium">{{ t('managementMcp.users') }}</h4>
            <label class="flex items-center gap-2"><input v-model="draft.allow_users" data-test="allow-users" type="checkbox" />{{ t('managementMcp.allowAccess') }}</label>
            <label class="flex items-center gap-2"><input v-model="draft.show_user_menu" data-test="user-menu" type="checkbox" />{{ t('managementMcp.showMenu') }}</label>
          </div>
        </div>
      </div>
      <button data-test="save" type="button" class="btn btn-primary" :disabled="!loaded || saving || (draft.enabled && !draft.public_url.trim())" @click="save">{{ saving ? t('common.processing') : t('common.save') }}</button>
    </fieldset>
    <button v-if="!loaded && !loading" type="button" class="btn btn-secondary" @click="load">{{ t('common.refresh') }}</button>
    <div class="space-y-2 border-t border-gray-100 pt-4 text-sm dark:border-dark-700">
      <p class="text-gray-600 dark:text-gray-300">{{ t('managementMcp.adminAnalysis') }}</p>
      <RouterLink class="text-primary-600 hover:underline dark:text-primary-400" to="/settings/mcp">{{ t('managementMcp.backToConnections') }}</RouterLink>
    </div>
    <TotpStepUpDialog :controller="stepUp" />
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { getManagementMCPSettings, updateManagementMCPSettings, type ManagementMCPSettings } from '@/api/admin/managementMcp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { isStepUpCancelled, useStepUp } from '@/composables/useStepUp'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const app = useAppStore()
const stepUp = useStepUp()
const draft = ref<ManagementMCPSettings>({ enabled: false, public_url: '', allow_operators: false, allow_users: false, show_operator_menu: false, show_user_menu: false })
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')

function message(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : (err as { message?: string } | null)?.message || fallback
}

async function load() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const result = await getManagementMCPSettings()
    draft.value = { ...result }
    loaded.value = true
  } catch (err) {
    error.value = message(err, t('managementMcp.adminLoadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!loaded.value || saving.value) return
  saving.value = true
  error.value = ''
  const input = { ...draft.value, public_url: draft.value.public_url.trim() }
  try {
    const result = await stepUp.run(() => updateManagementMCPSettings(input))
    draft.value = { ...result }
    app.showSuccess(t('managementMcp.adminSaved'))
    await app.fetchPublicSettings(true)
  } catch (err) {
    if (!isStepUpCancelled(err)) error.value = message(err, t('managementMcp.adminSaveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
