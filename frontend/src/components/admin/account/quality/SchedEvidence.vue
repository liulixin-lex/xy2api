<template>
  <div class="ev" :data-qp-evidence="dim">
    <ul class="ev__list">
      <!-- This group's and all the counted units (the rule's own set), each as recorded or unknown; kept apart from the
           evidence, so a record without one still shows the other. -->
      <li class="ev__i" data-qp-evidence-samples>{{ t(`admin.schedQuality.evidence.samples.${dim}`, split(samples.count, true)) }}</li>
      <li v-if="samples.weight" class="ev__i" data-qp-evidence-weight>{{ t('admin.schedQuality.evidence.samples.weight', split(samples.weight)) }}</li>
      <li v-for="item in items" :key="item.key" class="ev__i" :data-qp-evidence-item="item.key">
        {{ t(`admin.schedQuality.evidence.${dim}.${item.key}`, { v: item.value }) }}
      </li>
    </ul>
    <p v-if="samplesUnknown" class="ev__none" data-qp-evidence-samples-why>{{ t('admin.schedQuality.evidence.samples.unknownWhy') }}</p>
    <p v-if="!evidence" class="ev__none">{{ t('admin.schedQuality.evidence.none') }}</p>
    <template v-else>
      <p v-if="causes.length && !gateOff" class="ev__why" data-qp-evidence-why>
        {{ t('admin.schedQuality.evidence.why', { reasons: causes.join(t('admin.schedQuality.evidence.reasonJoiner')) }) }}
      </p>
      <p v-if="overLimit && !gateOff" class="ev__why" data-qp-evidence-over-limit>{{ t('admin.schedQuality.evidence.recoveryOverLimit') }}</p>
    </template>
    <p v-if="sources" class="ev__src">{{ t('admin.schedQuality.evidence.sources', { names: sources }) }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * The evidence of one line, as the backend computed it (the panel's expanded group row and a decision record):
 * this group's and all the units the rule counted, each field as a short sentence with its unit, why an unknown
 * first-output verdict is unknown, why an error line did not recover, and the groups the requests came from. Nothing is
 * judged here.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendDim, BackendEvidenceOf, BackendSampleCounts } from '@/api/admin/schedQualityTypes'
import { evidenceCauses, lineEvidence, lineSamples, type SampleSplit } from './qualityPanel'

const props = defineProps<{
  dim: BackendDim
  evidence: BackendEvidenceOf[BackendDim] | null | undefined
  /** The line or the basis: this group's and all the units the rule counted. */
  samples?: BackendSampleCounts | null
  sourceGroups?: number[]
  /** A group's name by id; the id when unknown. */
  groupName?: (id: number) => string | undefined
  /** The group's gate is off, so nothing here is judged: the head says so, and the statistical causes are not why. */
  gateOff?: boolean
}>()
const { t, te } = useI18n()

const items = computed(() => (props.evidence ? lineEvidence(props.dim, props.evidence) : []))
const samples = computed(() => lineSamples(props.dim, props.samples, props.evidence))
/** Each side as recorded or unknown; with `unit`, a known total carries the line's unit ("全部 30 条"), an unknown one none. */
const split = (value: SampleSplit, unit = false) => {
  const unknown = t('admin.schedQuality.evidence.samples.unknownValue')
  const total = value.total === null ? unknown : unit ? t(`admin.schedQuality.evidence.samples.total.${props.dim}`, { n: value.total }) : value.total
  return { local: value.local ?? unknown, total }
}
const samplesUnknown = computed(() =>
  [samples.value.count, samples.value.weight].some((value) => value && (value.local === null || value.total === null))
)
/** A review or a trial would have passed, but the failure share is above the limit: not recovered yet. */
const overLimit = computed(() => props.dim === 'error' && (props.evidence as BackendEvidenceOf['error'] | null | undefined)?.recovery_over_limit === true)
const causes = computed(() =>
  evidenceCauses(props.dim, props.evidence ?? undefined).map((cause) => {
    const key = `admin.schedQuality.evidence.causes.${cause}`
    return te(key) ? t(key) : t('admin.schedQuality.unknown')
  })
)
const sources = computed(() =>
  (props.sourceGroups ?? []).map((id) => props.groupName?.(id) ?? `#${id}`).join(t('admin.schedQuality.evidence.joiner'))
)
</script>

<style scoped>
.ev {
  display: grid;
  gap: 4px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
.ev__list {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 12px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-variant-numeric: tabular-nums;
}
.ev__why {
  color: var(--color-text-primary);
}
.ev__none,
.ev__src {
  color: var(--color-text-muted);
}
</style>
