<template>
  <div
    class="grid gap-3 py-4 lg:grid-cols-[minmax(0,1fr)_minmax(15rem,24rem)] lg:gap-8"
    :class="modified ? '-mx-3 rounded-xl bg-amber-50/70 px-3 dark:bg-amber-900/10' : ''"
    :data-test="`field-${policyKey}.${field.field}`"
    :data-modified="modified || undefined"
  >
    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <label :for="controlId" class="text-sm font-semibold text-gray-900 dark:text-white">{{ label }}</label>
        <code class="break-all text-xs text-gray-500 dark:text-dark-400">{{ field.field }}</code>
        <span v-if="modified" class="badge badge-warning">{{ t('admin.cacheSettings.modified') }}</span>
        <span v-if="masked" class="badge badge-gray" data-test="masked-by-master">
          <Icon name="lock" size="xs" aria-hidden="true" />
          {{ t('admin.cacheSettings.maskedByMaster') }}
        </span>
        <span v-if="clipped" class="badge badge-warning">{{ t('admin.cacheSettings.clipped') }}</span>
      </div>
      <p v-if="description" :id="descId" class="mt-1 max-w-prose text-sm text-gray-600 dark:text-dark-300">{{ description }}</p>
      <p v-if="impact" :id="impactId" class="mt-1.5 flex max-w-prose items-start gap-1.5 text-xs text-gray-700 dark:text-dark-200">
        <Icon name="exclamationCircle" size="xs" class="mt-0.5 shrink-0" aria-hidden="true" />
        <span><span class="font-semibold">{{ t('admin.cacheSettings.impact') }}</span>{{ impact }}</span>
      </p>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
        {{ t('admin.cacheSettings.defaultValue', { value: show(defaultValue) }) }}<template v-if="modified">{{ t('admin.cacheSettings.listSeparator') }}{{ t('admin.cacheSettings.originalValue', { value: show(storedValue) }) }}</template>
      </p>
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
    </div>
    <div class="min-w-0 lg:justify-self-end">
      <FieldControl
        :field="field"
        :value="value"
        :label="label"
        :control-id="controlId"
        :disabled="disabled || !!lock"
        :invalid="messages.some((m) => m.level === 'error')"
        :described-by="describedBy"
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
import { fieldCopyKey } from '../catalog'
import { codeLabel, formatValue } from '../format'
import { sameValue, type InlineIssue } from '../schema'
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
  masked: boolean
  clipped: boolean
  groups: AdminGroup[]
  groupsFailed: boolean
  fallbackGroupIds: Set<number>
}>()
const emit = defineEmits<{ (e: 'update', value: unknown): void }>()
const { t, te } = useI18n()

const slug = computed(() => `${props.policyKey}-${props.field.field}`.replace(/[^a-z0-9]+/gi, '-'))
const controlId = computed(() => `cache-policy-${slug.value}`)
const descId = computed(() => `cache-policy-desc-${slug.value}`)
const impactId = computed(() => `cache-policy-impact-${slug.value}`)
const issuesId = computed(() => `cache-policy-issues-${slug.value}`)

function copy(part: 'label' | 'description' | 'risk'): string {
  const key = fieldCopyKey(props.policyKey, props.field.field, part)
  return te(key) ? t(key) : ''
}
const label = computed(() => copy('label') || props.field.field)
const description = computed(() => copy('description'))
const impact = computed(() => copy('risk'))
const modified = computed(() => !sameValue(props.value, props.storedValue))

function groupName(id: number): string {
  const group = props.groups.find((g) => g.id === id)
  return group ? `${group.name} (#${id})` : t('admin.cacheSettings.groupPicker.groupID', { id })
}
function show(value: unknown): string {
  return formatValue(props.field, value, t, te, { group: groupName, account: (id) => t('admin.cacheSettings.ids.item', { id }) })
}

type MessageIcon = 'xCircle' | 'exclamationTriangle' | 'lock'
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
  const seen = new Set<string>()
  for (const error of props.serverErrors) {
    seen.add(error.code)
    list.push({
      key: `server-${error.code}`,
      code: error.code,
      level: 'error',
      text: codeLabel('errors', error.code, t, te, { limit: error.limit ?? '-', min: props.field.min ?? '-', max: props.field.max ?? '-' }),
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

const describedBy = computed(() =>
  [description.value ? descId.value : '', impact.value ? impactId.value : '', messages.value.length ? issuesId.value : ''].filter(Boolean).join(' ') || undefined,
)
</script>
