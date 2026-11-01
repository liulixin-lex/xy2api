<template>
  <div v-if="account.platform === 'openai'" class="inline-flex items-center gap-2" data-testid="iq-check-cell">
    <button type="button" role="switch" :aria-checked="enabled" :aria-label="t('admin.accounts.iqCheck')" :title="t('admin.accounts.iqToggleBuiltins')" :disabled="busy" class="relative inline-flex h-5 w-9 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-50" :class="enabled ? 'bg-blue-500' : 'bg-gray-200 dark:bg-dark-600'" @click="$emit('toggle', !enabled)">
      <span class="pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow transition-transform" :class="enabled ? 'translate-x-4' : 'translate-x-0'" />
    </button>
    <!-- 每个模型一行：名称与结论各成一列左对齐；开关相对整组垂直居中，1 个模型时与开关同一行。 -->
    <button type="button" class="grid min-h-8 grid-cols-[auto_auto] content-center items-center gap-x-1.5 gap-y-0.5 whitespace-nowrap rounded px-1 py-1 text-left text-xs leading-4 hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 dark:hover:bg-dark-700" :title="t('admin.accounts.iqRecords')" :aria-label="t('admin.accounts.iqOpenRecords', { status: tags.map(tag => `${tag.name} ${tag.state}`).join(', ') || t('admin.accounts.iqOff') })" @click="$emit('records')">
      <span v-if="!tags.length" class="col-span-2 text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqOff') }}</span>
      <span v-for="tag in visibleTags" :key="tag.model" class="contents" data-testid="iq-target-tag">
        <span class="max-w-[5rem] truncate text-gray-500 dark:text-dark-400" :title="tag.model" data-part="name">{{ tag.name }}</span>
        <span class="inline-flex items-center gap-1 font-medium" :class="IQ_TONE_CLASSES[tag.tone]" data-part="state">
          <Icon v-if="tag.spinning" name="refresh" size="xs" class="animate-spin opacity-70" aria-hidden="true" />{{ tag.state }}
        </span>
      </span>
      <span v-if="hiddenTags.length" class="col-span-2 font-medium" :class="IQ_TONE_CLASSES[hiddenTone]" data-testid="iq-target-more">+{{ hiddenTags.length }}</span>
    </button>
  </div>
  <span v-else class="text-xs text-gray-400">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Account, IQTargetSummary } from '@/types'
import { isIQBuiltinModel, iqModelLabel, sortIQTargets } from '@/components/admin/account/iqCheckPresentation'

const props = defineProps<{ account: Account; busy?: boolean }>()
// toggle carries the requested state for both built-in targets.
defineEmits<{ toggle: [enabled: boolean]; records: [] }>()
const { t } = useI18n()
const targets = computed(() => sortIQTargets(props.account.iq_targets ?? []))
const enabled = computed(() => targets.value.some(target => isIQBuiltinModel(target.model) && target.enabled))

// Text colour only, no pill backgrounds; ordered from most to least severe for the "+N" summary.
// In-progress states use the column's blue (same as the switch) so they never read as a verdict.
type Tone = 'danger' | 'warning' | 'active' | 'ok' | 'neutral'
const TONE_RANK: Tone[] = ['danger', 'warning', 'active', 'ok', 'neutral']
const IQ_TONE_CLASSES: Record<Tone, string> = {
  danger: 'text-red-700 dark:text-red-400',
  warning: 'text-amber-700 dark:text-amber-400',
  active: 'text-blue-700 dark:text-blue-400',
  ok: 'text-emerald-700 dark:text-emerald-400',
  neutral: 'text-gray-600 dark:text-dark-300'
}

// The cooldown itself (and its recheck time) is shown once, in the status column; here only the verdict.
function tag(target: IQTargetSummary): { state: string; tone: Tone; spinning?: boolean } {
  if (target.state === 'running') return { state: t('admin.accounts.iqStates.running'), tone: 'active', spinning: true }
  if (target.state === 'queued') return { state: t('admin.accounts.iqStates.queued'), tone: 'active' }
  if (target.gate_active) return { state: t('admin.accounts.iqCheckStatus.degraded'), tone: 'danger' }
  if (target.state === 'paused') return { state: t('admin.accounts.iqStates.paused'), tone: 'warning' }
  const tone: Tone = target.verdict === 'smart' ? 'ok' : target.verdict === 'degraded' ? 'danger' : 'neutral'
  return { state: t('admin.accounts.iqCheckStatus.' + target.verdict), tone }
}
// Disabled targets stay hidden unless they still hold a gate or are being checked manually.
const tags = computed(() => targets.value
  .filter(target => target.enabled || target.gate_active || target.state === 'queued' || target.state === 'running')
  .map(target => ({ model: target.model, name: iqModelLabel(target.model), ...tag(target) })))

// At most three lines: with four or more targets, two rows plus a "+N" coloured by the worst hidden one.
const MAX_LINES = 3
const visibleTags = computed(() => (tags.value.length > MAX_LINES ? tags.value.slice(0, MAX_LINES - 1) : tags.value))
const hiddenTags = computed(() => tags.value.slice(visibleTags.value.length))
const hiddenTone = computed<Tone>(() => TONE_RANK.find(tone => hiddenTags.value.some(item => item.tone === tone)) ?? 'neutral')
</script>
