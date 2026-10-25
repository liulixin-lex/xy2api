<template>
  <div class="w-full max-w-2xl" data-test="threshold-rules">
    <template v-if="editable">
      <div v-if="rules.length" class="overflow-x-auto">
        <table class="w-full text-sm">
          <caption class="sr-only">{{ label }}</caption>
          <thead>
            <tr class="text-left text-xs text-gray-500 dark:text-dark-400">
              <th scope="col" class="pb-1 pr-2 font-medium">{{ t('admin.cacheSettings.rules.model') }}</th>
              <th scope="col" class="pb-1 pr-2 font-medium">{{ t('admin.cacheSettings.rules.match') }}</th>
              <th scope="col" class="pb-1 pr-2 font-medium">{{ t('admin.cacheSettings.rules.threshold') }}</th>
              <th scope="col" class="pb-1"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(rule, index) in rules" :key="index">
              <td class="py-1 pr-2">
                <input
                  type="text"
                  class="input py-1.5 font-mono"
                  :value="rule.model"
                  :placeholder="t('admin.cacheSettings.rules.modelPlaceholder')"
                  :disabled="disabled"
                  :aria-label="`${t('admin.cacheSettings.rules.model')} ${index + 1}`"
                  spellcheck="false"
                  @change="patch(index, { model: ($event.target as HTMLInputElement).value.trim() })"
                />
              </td>
              <td class="py-1 pr-2">
                <select
                  class="input py-1.5"
                  :value="rule.match"
                  :disabled="disabled"
                  :aria-label="`${t('admin.cacheSettings.rules.match')} ${index + 1}`"
                  @change="patch(index, { match: ($event.target as HTMLSelectElement).value as ThresholdRule['match'] })"
                >
                  <option value="exact">{{ t('admin.cacheSettings.rules.exact') }}</option>
                  <option value="prefix">{{ t('admin.cacheSettings.rules.prefix') }}</option>
                </select>
              </td>
              <td class="py-1 pr-2">
                <select
                  v-if="allowedValues.length"
                  class="input py-1.5 tabular-nums"
                  :value="String(rule.threshold)"
                  :disabled="disabled"
                  :aria-label="`${t('admin.cacheSettings.rules.threshold')} ${index + 1}`"
                  @change="patch(index, { threshold: Number(($event.target as HTMLSelectElement).value) })"
                >
                  <option v-if="!allowedValues.includes(rule.threshold)" :value="String(rule.threshold)">{{ rule.threshold }}</option>
                  <option v-for="v in allowedValues" :key="v" :value="String(v)">{{ v }}</option>
                </select>
                <input
                  v-else
                  type="number"
                  class="input w-28 py-1.5 text-right tabular-nums"
                  :value="rule.threshold"
                  :disabled="disabled"
                  :aria-label="`${t('admin.cacheSettings.rules.threshold')} ${index + 1}`"
                  @change="patch(index, { threshold: Number(($event.target as HTMLInputElement).value) })"
                />
              </td>
              <td class="py-1 text-right">
                <button
                  type="button"
                  class="btn btn-ghost btn-sm"
                  :disabled="disabled"
                  :aria-label="t('admin.cacheSettings.rules.remove', { row: index + 1 })"
                  @click="remove(index)"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.cacheSettings.rules.empty') }}</p>
      <button type="button" class="btn btn-secondary btn-sm mt-2" :disabled="disabled" @click="add">
        <Icon name="plus" size="sm" />
        {{ t('admin.cacheSettings.rules.add') }}
      </button>
    </template>
    <template v-else>
      <p class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.cacheSettings.rules.readOnly') }}</p>
      <pre class="mt-2 max-h-48 overflow-auto rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-dark-900 dark:text-dark-200">{{ JSON.stringify(value, null, 2) }}</pre>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

interface ThresholdRule {
  model: string
  match: 'exact' | 'prefix'
  threshold: number
}

const props = defineProps<{ value: unknown; disabled: boolean; label: string; allowed?: number[] }>()
const emit = defineEmits<{ (e: 'update', value: ThresholdRule[]): void }>()
const { t } = useI18n()

const allowedValues = computed(() => props.allowed ?? [])

function isThresholdRuleList(value: unknown): value is ThresholdRule[] {
  return (
    Array.isArray(value) &&
    value.every(
      (r) =>
        r !== null &&
        typeof r === 'object' &&
        Object.keys(r).sort().join(',') === 'match,model,threshold' &&
        typeof (r as ThresholdRule).model === 'string' &&
        ((r as ThresholdRule).match === 'exact' || (r as ThresholdRule).match === 'prefix') &&
        typeof (r as ThresholdRule).threshold === 'number',
    )
  )
}
const editable = computed(() => isThresholdRuleList(props.value))
const rules = computed<ThresholdRule[]>(() => (isThresholdRuleList(props.value) ? props.value : []))

function patch(index: number, change: Partial<ThresholdRule>) {
  emit('update', rules.value.map((r, i) => (i === index ? { ...r, ...change } : { ...r })))
}
function remove(index: number) {
  emit('update', rules.value.filter((_, i) => i !== index).map((r) => ({ ...r })))
}
function add() {
  emit('update', [...rules.value.map((r) => ({ ...r })), { model: '', match: 'prefix', threshold: allowedValues.value[0] ?? 1024 }])
}
</script>
