<template>
  <div
    class="grid gap-3 py-4 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-8"
    :class="modified ? '-mx-3 rounded-xl bg-amber-50/70 px-3 dark:bg-amber-900/10' : ''"
    :data-test="`field-${policyKey}.${field.field}`"
    :data-modified="modified || undefined"
  >
    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <!-- Only controls that carry controlId get a <label for>; the others name themselves (fieldset legend, aria-label). -->
        <component :is="labelable ? 'label' : 'span'" :for="labelable ? controlId : undefined" class="text-sm font-semibold text-gray-900 dark:text-white">{{ label }}</component>
        <span v-if="modified" class="badge badge-warning">{{ t('admin.cacheSettings.modified') }}</span>
        <span v-if="clipped" class="badge badge-warning">{{ t('admin.cacheSettings.clipped') }}</span>
      </div>
      <p v-if="description" :id="descId" class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ description }}</p>
      <p v-if="modified" :id="origId" class="mt-1 text-xs text-gray-600 dark:text-dark-300" data-test="field-original">{{ t('admin.cacheSettings.originalValue', { value: show(storedValue) }) }}</p>
      <ul v-if="messages.length" :id="issuesId" class="mt-2 space-y-1" role="list">
        <li
          v-for="message in messages"
          :key="message.key"
          class="flex max-w-prose items-start gap-1.5 text-xs"
          :class="message.tone"
          :data-issue="message.code"
        >
          <Icon :name="message.icon" size="xs" class="mt-0.5 shrink-0" aria-hidden="true" />
          <span>{{ message.text }}</span>
        </li>
      </ul>
      <!-- Impact, default and cap source on demand: the field row keeps label, description and problems (m-6). -->
      <details class="group mt-1.5 max-w-prose text-xs text-gray-600 dark:text-dark-300" data-test="field-details">
        <summary
          class="inline-flex cursor-pointer list-none items-center gap-1 rounded text-gray-600 hover:text-gray-900 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-dark-300 dark:hover:text-white [&::-webkit-details-marker]:hidden"
        >
          <Icon name="chevronRight" size="xs" class="transition-transform group-open:rotate-90 motion-reduce:transition-none" aria-hidden="true" />
          {{ t('admin.cacheSettings.details') }}
        </summary>
        <dl class="mt-1.5 space-y-1 border-l-2 border-gray-200 pl-3 dark:border-dark-600">
          <div v-if="impact" class="flex gap-1">
            <dt class="shrink-0 font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheSettings.impact') }}</dt>
            <dd>{{ impact }}</dd>
          </div>
          <div class="flex gap-1">
            <dt class="shrink-0">{{ t('admin.cacheSettings.defaultLabel') }}</dt>
            <dd>{{ show(defaultValue) }}</dd>
          </div>
          <div v-if="field.cap_source" class="flex gap-1">
            <dt class="shrink-0">{{ t('admin.cacheSettings.capLabel') }}</dt>
            <dd class="break-all font-mono">{{ field.cap_source }}</dd>
          </div>
          <div class="flex gap-1">
            <dt class="shrink-0">{{ t('admin.cacheSettings.fieldKey') }}</dt>
            <dd class="break-all font-mono">{{ field.field }}</dd>
          </div>
        </dl>
      </details>
    </div>
    <div class="min-w-0">
      <FieldControl
        :field="field"
        :value="value"
        :label="label"
        :control-id="controlId"
        :disabled="disabled || !!lock"
        :invalid="messages.some((m) => m.level === 'error')"
        :described-by="describedBy"
        :empty-hint="emptyHint"
        :groups="groups"
        :groups-failed="groupsFailed"
        :fallback-group-ids="fallbackGroupIds"
        @update="emit('update', $event)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminGroup } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { controlIdFor, fieldCopyKey } from '../catalog'
import { codeLabel, errorParams, formatValue, idNames } from '../format'
import { fieldKind, sameValue, type InlineIssue } from '../schema'
import type { FieldError, SchemaField } from '../types'
import FieldControl from './FieldControl.vue'

const props = defineProps<{
  policyKey: string
  field: SchemaField
  value: unknown
  storedValue: unknown
  defaultValue: unknown
  issues: InlineIssue[]
  serverErrors: FieldError[]
  lock: string | null
  disabled: boolean
  clipped: boolean
  groups: AdminGroup[]
  groupsFailed: boolean
  fallbackGroupIds: Set<number>
}>()
const emit = defineEmits<{ (e: 'update', value: unknown): void }>()
const { t, te } = useI18n()

const slug = computed(() => `${props.policyKey}-${props.field.field}`.replace(/[^a-z0-9]+/gi, '-'))
const controlId = computed(() => controlIdFor(props.policyKey, props.field.field))
const descId = computed(() => `cache-policy-desc-${slug.value}`)
const issuesId = computed(() => `cache-policy-issues-${slug.value}`)
const origId = computed(() => `cache-policy-orig-${slug.value}`)

function copy(part: 'label' | 'description' | 'risk' | 'empty'): string {
  const key = fieldCopyKey(props.policyKey, props.field.field, part)
  return te(key) ? t(key) : ''
}
const label = computed(() => copy('label') || props.field.field)
// What an empty id list means differs per field (all eligible accounts, or no effect); copy without it keeps the generic hint.
const emptyHint = computed(() => copy('empty'))
const description = computed(() => copy('description'))
const impact = computed(() => copy('risk'))
const modified = computed(() => !sameValue(props.value, props.storedValue))
const labelable = computed(() => !['enum_list', 'threshold_rules', 'unknown'].includes(fieldKind(props.field)))

function show(value: unknown): string {
  return formatValue(props.field, value, t, te, idNames(props.field, props.groups, t))
}

type MessageIcon = 'xCircle' | 'exclamationTriangle' | 'lock' | 'infoCircle'
interface Message {
  key: string
  code: string
  level: 'error' | 'warning' | 'info'
  text: string
  tone: string
  icon: MessageIcon
}

const messages = computed<Message[]>(() => {
  const list: Message[] = []
  if (props.lock) {
    list.push({
      key: `lock-${props.lock}`,
      code: `locked.${props.lock}`,
      level: 'info',
      text: codeLabel('locked', props.lock, t, te),
      tone: 'text-gray-600 dark:text-dark-300',
      icon: 'lock',
    })
  }
  // A stored value above a lowered cap runs at the cap; say what actually runs (C-10).
  if (props.clipped && typeof props.storedValue === 'number' && props.field.max !== undefined && props.storedValue > props.field.max) {
    list.push({
      key: 'clipped',
      code: 'clipped',
      level: 'info',
      text: t('admin.cacheSettings.clippedRunsAt', { value: show(props.field.max) }),
      tone: 'text-gray-700 dark:text-dark-200',
      icon: 'infoCircle',
    })
  }
  const seen = new Set<string>()
  for (const error of props.serverErrors) {
    seen.add(error.code)
    list.push({
      key: `server-${error.code}`,
      code: error.code,
      level: 'error',
      text: codeLabel('errors', error.code, t, te, errorParams(props.field, error)),
      tone: 'text-red-700 dark:text-red-300',
      icon: 'xCircle',
    })
  }
  for (const issue of props.issues) {
    if (seen.has(issue.code)) continue
    list.push({
      key: `inline-${issue.code}`,
      code: issue.code,
      level: issue.level,
      text: codeLabel('errors', issue.code, t, te, issue.params),
      tone: issue.level === 'error' ? 'text-red-700 dark:text-red-300' : 'text-amber-800 dark:text-amber-300',
      icon: issue.level === 'error' ? 'xCircle' : 'exclamationTriangle',
    })
  }
  return list
})

const describedBy = computed(
  () => [description.value ? descId.value : '', modified.value ? origId.value : '', messages.value.length ? issuesId.value : ''].filter(Boolean).join(' ') || undefined,
)
</script>
