<template>
  <BaseDialog :show="reset.accountId !== null" :title="t('admin.schedQuality.action.reset')" :width="result ? 'normal' : 'narrow'" @close="close">
    <p class="text-sm font-medium text-gray-900 dark:text-white">{{ reset.accountName }}</p>
    <div v-if="result" class="mt-4 grid gap-4 sm:grid-cols-3" data-sq-reset-result>
      <section v-for="column in columns" :key="column.key" :data-column="column.key">
        <h4 class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ column.title }}</h4>
        <ul v-if="column.items.length" class="mt-1.5 space-y-1 text-sm text-gray-900 dark:text-gray-100">
          <li v-for="item in column.items" :key="item.key">
            {{ kindName(item.kind, t, te) }}<span v-if="modelFromResetKey(item.key, item.kind)" class="ml-1.5 sq-text-3">{{ modelFromResetKey(item.key, item.kind) }}</span>
          </li>
        </ul>
        <p v-else class="mt-1.5 text-sm sq-text-3">—</p>
      </section>
    </div>
    <p v-else-if="failed" class="mt-3 sq-fail-word">{{ t('admin.schedQuality.result.failed') }}</p>
    <template #footer>
      <template v-if="result">
        <button type="button" class="btn btn-secondary btn-sm" @click="close">{{ t('admin.schedQuality.action.close') }}</button>
      </template>
      <template v-else>
        <button type="button" class="btn btn-secondary btn-sm" @click="close">{{ t('admin.schedQuality.action.cancel') }}</button>
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy" :aria-busy="busy" data-sq-reset-confirm @click="run">
          <span v-if="busy" class="spinner h-3.5 w-3.5" aria-hidden="true" />{{ t('admin.schedQuality.action.reset') }}
        </button>
      </template>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedResetResult } from '@/types/schedQuality'
import { closeSchedReset, markSchedChanged, useSchedLayers } from '@/composables/useSchedLayers'
import { kindName, modelFromResetKey } from '@/utils/schedQualityFormat'

// Reset (design spec B.9): one line with the account name, then the result in the same dialog as three columns —
// cleared, kept, failed — of short state names. The same flow serves the drawer, the row menu and the
// temporary-pause dialog.
const emit = defineEmits<{ done: [accountId: number] }>()
const { t, te } = useI18n()
const { reset } = useSchedLayers()
const busy = ref(false)
const failed = ref(false)
const result = ref<SchedResetResult | null>(null)
let doneFor: number | null = null

watch(() => reset.accountId, () => {
  busy.value = false
  failed.value = false
  result.value = null
})

const columns = computed(() => {
  const r = result.value
  if (!r) return []
  return [
    { key: 'cleared', title: t('admin.schedQuality.reset.cleared'), items: r.cleared },
    { key: 'preserved', title: t('admin.schedQuality.reset.preserved'), items: r.preserved },
    { key: 'failed', title: t('admin.schedQuality.result.failed'), items: r.failed }
  ]
})

async function run() {
  const id = reset.accountId
  if (id === null || busy.value) return
  busy.value = true
  failed.value = false
  try {
    const response = await schedQualityAPI.reset(id)
    if (reset.accountId !== id) return
    result.value = response
    doneFor = id
    markSchedChanged()
  } catch {
    if (reset.accountId === id) failed.value = true
  } finally {
    if (reset.accountId === id) busy.value = false
  }
}

function close() {
  const id = doneFor
  doneFor = null
  closeSchedReset()
  if (id !== null) emit('done', id)
}
</script>
