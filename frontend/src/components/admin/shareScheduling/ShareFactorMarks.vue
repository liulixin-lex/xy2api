<template>
  <div v-if="marks.length" class="mt-1 flex flex-wrap justify-end gap-1" :data-testid="'share-factors-' + member.account_id">
    <HelpTooltip v-for="mark in marks" :key="mark.kind" :width-class="mark.kind === 'insufficient' ? 'w-72' : 'w-80'">
      <template #trigger>
        <!-- A button so keyboards reach it: activating it expands the breakdown below; screen readers get it as the description. -->
        <button
          type="button"
          class="cursor-help rounded px-1.5 py-0.5 text-[11px] focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
          :class="mark.className"
          :aria-describedby="uid + '-' + mark.kind"
          :aria-expanded="expanded === mark.kind"
          :aria-controls="expanded === mark.kind ? uid + '-expanded' : undefined"
          :data-testid="'share-factor-' + mark.kind + '-' + member.account_id"
          @click="expanded = expanded === mark.kind ? null : mark.kind"
        >{{ mark.label }}</button>
      </template>
      <div class="space-y-1" :data-testid="mark.kind === 'health' ? 'share-factor-detail-' + member.account_id : undefined">
        <p v-for="(line, index) in mark.lines" :key="index" :class="index === 0 && mark.kind !== 'insufficient' ? 'font-medium' : ''">{{ line }}</p>
      </div>
    </HelpTooltip>
    <span v-for="mark in marks" :id="uid + '-' + mark.kind" :key="'sr-' + mark.kind" class="sr-only">{{ mark.lines.join(' ') }}</span>
    <div
      v-if="expandedMark"
      :id="uid + '-expanded'"
      class="w-full space-y-0.5 rounded-lg bg-gray-50 px-2 py-1.5 text-left text-[11px] leading-snug text-gray-700 dark:bg-dark-700/60 dark:text-dark-200"
      :data-testid="'share-factor-expanded-' + member.account_id"
    >
      <p v-for="(line, index) in expandedMark.lines" :key="index">{{ line }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { ShareMember } from '@/api/admin/shareScheduling'
import { formatShareFactor, formatShareWeight, shareFactors } from '@/utils/shareFactors'
import { formatSharePercent } from '@/utils/shareScheduling'

/**
 * Marks next to an account's expected share when an automatic factor
 * changes it. The breakdown shows on hover, expands in place when a mark is
 * activated from the keyboard, and is the mark's description for screen
 * readers.
 */
const props = withDefaults(defineProps<{
  member: ShareMember
  now?: number
  /** Standby accounts take no new sessions, so "not enough data" says nothing there. */
  standby?: boolean
}>(), { now: undefined, standby: false })

type MarkKind = 'health' | 'ramp' | 'insufficient'

interface Mark {
  kind: MarkKind
  label: string
  className: string
  lines: string[]
}

const uid = `share-factor-${useId()}`

const { t, te } = useI18n()
const view = computed(() => shareFactors(props.member, props.now))
const expanded = ref<MarkKind | null>(null)

const breakdown = computed(() => t('admin.shareScheduling.traffic.factors.breakdown', {
  loadFactor: props.member.effective_load_factor,
  health: formatShareFactor(props.member.health_factor),
  ramp: formatShareFactor(props.member.ramp_factor),
  weight: formatShareWeight(props.member.effective_weight),
  share: formatSharePercent(props.member.expected_share),
  base: formatSharePercent(props.member.base_share)
}))

const healthReason = computed(() => {
  const health = view.value.health
  if (!health) return ''
  return t('admin.shareScheduling.traffic.factors.healthReason', {
    minutes: health.windowMinutes,
    rate: formatSharePercent(health.rate),
    baseline: formatSharePercent(health.baseline)
  })
})

const rampReason = computed(() => {
  const ramp = view.value.ramp
  if (!ramp) return ''
  const causeKey = `admin.shareScheduling.traffic.factors.rampReasons.${ramp.cause}`
  const cause = ramp.cause && ramp.sinceMinutes !== null && (typeof te !== 'function' || te(causeKey))
    ? t(causeKey, { since: ramp.sinceMinutes })
    : ''
  const remaining = ramp.remainingMinutes === null ? '' : t('admin.shareScheduling.traffic.factors.rampRemaining', { minutes: ramp.remainingMinutes })
  return `${cause}${remaining}`
})

const marks = computed<Mark[]>(() => {
  const out: Mark[] = []
  const v = view.value
  if (v.health) {
    out.push({
      kind: 'health',
      label: t('admin.shareScheduling.traffic.factors.health', { value: formatShareFactor(v.health.factor) }),
      className: 'bg-amber-50 font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300',
      lines: [breakdown.value, healthReason.value, rampReason.value].filter(Boolean)
    })
  }
  if (v.ramp) {
    out.push({
      kind: 'ramp',
      label: v.ramp.remainingMinutes === null
        ? t('admin.shareScheduling.traffic.factors.rampNoTime', { value: formatShareFactor(v.ramp.factor) })
        : t('admin.shareScheduling.traffic.factors.ramp', { value: formatShareFactor(v.ramp.factor), minutes: v.ramp.remainingMinutes }),
      className: 'bg-sky-50 font-medium text-sky-700 dark:bg-sky-900/20 dark:text-sky-300',
      lines: [breakdown.value, healthReason.value, rampReason.value].filter(Boolean)
    })
  }
  if (v.insufficient && !v.health && !props.standby) {
    out.push({
      kind: 'insufficient',
      label: t('admin.shareScheduling.traffic.factors.insufficient'),
      className: 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300',
      lines: [t('admin.shareScheduling.traffic.factors.insufficientReason', { minutes: props.member.health?.window_minutes ?? 30 })]
    })
  }
  return out
})

const expandedMark = computed(() => marks.value.find(mark => mark.kind === expanded.value) ?? null)
</script>
