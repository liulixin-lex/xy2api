<template>
  <dl class="ev" :data-qp-evidence="basis.line">
    <!-- When the evidence was taken, then what it read against the line, as the panel's cell and the list's card read it. -->
    <div v-if="figures.period || figures.reading" class="ev__row" data-qp-evidence-measure>
      <dt v-if="figures.period" data-qp-evidence-period>{{ figures.period }}</dt>
      <!-- The reading under the panel's column name (首字慢: a share, not seconds), as on the dot's card. -->
      <template v-if="figures.reading">
        <dt data-qp-evidence-line>{{ t(`admin.schedQuality.panel.col.${basis.line}`) }}</dt>
        <dd class="ev__main" :class="`is-${tone}`" data-qp-evidence-reading>{{ figures.reading }}</dd>
      </template>
    </div>
    <div v-if="figures.samples.length" class="ev__row" data-qp-evidence-samples>
      <template v-for="item in figures.samples" :key="item.label">
        <dt>{{ label(item) }}</dt>
        <dd>{{ item.value }}</dd>
      </template>
    </div>
  </dl>
</template>

<script setup lang="ts">
/**
 * What one decision was taken on (basisFigures): when its evidence was taken, then the line by the panel's column name
 * and what it read against it in the action's colour ("13:05–13:15  缓存  76.2 / 86", the same reading as the panel's
 * cell and the list's card), then the units counted. No sentences; the full record stays with the backend.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendBasis } from '@/api/admin/schedQualityTypes'
import { basisFigures, type BasisFigure } from './qualityPanel'

const props = defineProps<{
  basis: BackendBasis
  /** The action's colour: rear, pass, trial or muted. */
  tone: string
}>()
const { t } = useI18n()

const figures = computed(() => basisFigures(props.basis))
const label = (item: BasisFigure) => t(`admin.schedQuality.evidence.${item.label}`)
</script>

<style scoped>
/* Two short lines of label and figure pairs, the figures in tabular digits. */
.ev {
  display: grid;
  gap: 2px;
  margin: 0;
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
.ev__row {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0 6px;
}
.ev dt {
  color: var(--color-text-muted);
}
.ev dd {
  margin: 0 8px 0 0;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}
.ev__main {
  font-weight: var(--font-weight-medium);
}
.ev__main.is-rear {
  color: var(--color-status-back-text);
}
.ev__main.is-pass {
  color: var(--color-status-ok-text);
}
.ev__main.is-trial {
  color: var(--color-status-info-text);
}
.ev__main.is-muted {
  color: var(--color-text-primary);
}
</style>
