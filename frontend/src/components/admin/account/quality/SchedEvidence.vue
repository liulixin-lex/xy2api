<template>
  <div class="ev" :data-qp-evidence="dim">
    <p v-if="!evidence" class="ev__none">{{ t('admin.schedQuality.evidence.none') }}</p>
    <template v-else>
      <ul class="ev__list">
        <li v-for="item in items" :key="item.key" class="ev__i" :data-qp-evidence-item="item.key">
          {{ t(`admin.schedQuality.evidence.${dim}.${item.key}`, { v: item.value }) }}
        </li>
      </ul>
      <p v-if="causes.length" class="ev__why" data-qp-evidence-why>
        {{ t('admin.schedQuality.evidence.why', { reasons: causes.join(t('admin.schedQuality.evidence.reasonJoiner')) }) }}
      </p>
    </template>
    <p v-if="sources" class="ev__src">{{ t('admin.schedQuality.evidence.sources', { names: sources }) }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * The evidence of one line, as the backend computed it (the panel's expanded group row and a decision record):
 * each field as a short sentence with its unit, why an unknown first-output verdict is unknown, and the groups the
 * requests came from. Nothing is judged here.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendDim, BackendEvidenceOf } from '@/api/admin/schedQualityTypes'
import { evidenceCauses, lineEvidence } from './qualityPanel'

const props = defineProps<{
  dim: BackendDim
  evidence: BackendEvidenceOf[BackendDim] | null | undefined
  sourceGroups?: number[]
  /** A group's name by id; the id when unknown. */
  groupName?: (id: number) => string | undefined
}>()
const { t, te } = useI18n()

const items = computed(() => (props.evidence ? lineEvidence(props.dim, props.evidence) : []))
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
