<template>
  <section class="card p-6" data-testid="share-scheduling-settings" aria-labelledby="share-scheduling-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h2 id="share-scheduling-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.settings.title') }}</h2>
        <p class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.settings.hint') }}</p>
      </div>
      <RouterLink to="/admin/share-scheduling" class="btn btn-secondary">{{ t('admin.shareScheduling.settings.openPage') }}</RouterLink>
    </div>
    <div v-if="loading && enabled === null" class="mt-5 h-10 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" role="status" :aria-label="t('common.loading')" />
    <div v-else-if="loadFailed" class="mt-4 flex flex-wrap items-center gap-3" role="alert">
      <p class="text-sm text-red-700 dark:text-red-300">{{ t('admin.shareScheduling.global.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ t('admin.shareScheduling.global.reload') }}</button>
    </div>
    <div v-else-if="enabled !== null" class="mt-5 flex items-start justify-between gap-4">
      <div>
        <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.shareScheduling.global.title') }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.global.hint') }}</p>
      </div>
      <ShareGlobalSwitch v-model="enabled" :labelled="false" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { adminAPI } from '@/api/admin'
import ShareGlobalSwitch from '@/components/admin/shareScheduling/ShareGlobalSwitch.vue'

const { t } = useI18n()
const enabled = ref<boolean | null>(null)
const loading = ref(false)
const loadFailed = ref(false)
let disposed = false

async function load() {
  if (loading.value) return
  loading.value = true
  loadFailed.value = false
  try {
    const settings = await adminAPI.shareScheduling.getSettings()
    if (disposed) return
    if (typeof settings?.enabled !== 'boolean') throw new Error('unexpected share scheduling settings')
    enabled.value = settings.enabled
  } catch {
    if (!disposed) loadFailed.value = true
  } finally {
    if (!disposed) loading.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>
