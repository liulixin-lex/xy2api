<template>
  <div class="acard" :class="{ 'is-selected': selected }" data-test="account-card">
    <div class="acard__head">
      <slot name="check" />
      <div class="acard__title"><slot name="name" /></div>
      <div v-if="$slots.status" class="acard__status"><slot name="status" /></div>
    </div>
    <div class="acard__bar">
      <div v-if="$slots.sched" class="acard__sched"><span aria-hidden="true">{{ schedLabel }}</span><slot name="sched" /></div>
      <div class="acard__ops"><slot name="ops" /></div>
    </div>
    <section v-if="$slots.groups" class="acard__sec" data-groups-section>
      <!-- The group picker opens under this row (its "edit groups" button), as in the prototype. -->
      <div class="acard__sec-head" data-groups-anchor><span class="acard__label">{{ groupsLabel }}</span><slot name="groups-action" /></div>
      <slot name="groups" />
    </section>
    <section v-if="$slots.recent" class="acard__sec">
      <div class="acard__sec-head"><span class="acard__label">{{ recentLabel }}</span></div>
      <slot name="recent" />
    </section>
    <div v-if="rows.length && expanded" :id="moreId" class="acard__sec acard__more" data-test="account-card-more">
      <div v-for="item in rows" :key="item.key" class="acard__row" :data-field="item.key">
        <span class="acard__label">{{ item.label }}</span>
        <div class="acard__value"><slot name="row" :field="item.key" /></div>
      </div>
    </div>
    <button
      v-if="rows.length"
      type="button"
      class="acard__toggle"
      :aria-expanded="expanded ? 'true' : 'false'"
      :aria-controls="moreId"
      data-test="account-card-toggle"
      @click="emit('toggle')"
    >
      {{ expanded ? t('admin.accounts.card.collapse') : t('admin.accounts.card.expand') }}<Icon name="chevronDown" size="xs" class="acard__chev" />
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * One account below 1024px (spec 4.6, prototype "窄屏卡片"), in two layers. Always shown: the check box, name with
 * "platform · type", the status at the head's right, the scheduling switch and the row actions, the groups grid and
 * the recent status. Expanded (the page keeps which accounts are open, so a refresh does not fold them): one line per
 * remaining column, label and value (IQ check, capacity, upstream ratio, today's stats, then any optional column the
 * user shows). The page fills every slot with the table's own cells, so a card reads, dims and updates like its row.
 */
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

defineProps<{
  /** The expanded layer's lines, in column order. */
  rows: Array<{ key: string; label: string }>
  expanded: boolean
  selected?: boolean
  groupsLabel?: string
  recentLabel?: string
  schedLabel?: string
}>()
const emit = defineEmits<{ toggle: [] }>()
const { t } = useI18n()
const moreId = `acard-more-${useId()}`
</script>

<style scoped>
.acard {
  border-radius: inherit;
}
.acard.is-selected {
  background: var(--color-bg-selected);
}
.acard__head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px 16px 8px;
}
.acard__title {
  flex: 1;
  min-width: 0;
}
.acard__status {
  flex: none;
  max-width: 50%;
  min-width: 0;
}
.acard__bar {
  display: flex;
  min-height: 40px;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 12px 8px 16px;
}
.acard__sched {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.acard__ops {
  display: flex;
  margin-left: auto;
  gap: 4px;
}
/* Touch-sized row actions (at least 40px). */
.acard__ops :slotted(button) {
  width: 40px;
  height: 40px;
}
.acard__sec {
  padding: 12px 16px;
  border-top: 1px solid var(--color-line-row);
}
.acard__sec-head {
  display: flex;
  min-height: 24px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
.acard__label {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
.acard__more {
  display: grid;
  gap: 12px;
}
.acard__row {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr);
  align-items: start;
  gap: 12px;
}
.acard__row > .acard__label {
  padding-top: 2px;
}
.acard__value {
  min-width: 0;
  overflow-wrap: anywhere;
}
.acard__toggle {
  display: flex;
  width: 100%;
  height: 44px;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 0;
  border-top: 1px solid var(--color-line-row);
  border-radius: 0 0 var(--radius-lg) var(--radius-lg);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
}
.acard__toggle:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  .acard__toggle:hover {
    background: var(--color-bg-row-hover);
    color: var(--color-text-primary);
  }
}
.acard__chev {
  transition: transform var(--motion-base) var(--ease-out);
}
.acard__toggle[aria-expanded='true'] .acard__chev {
  transform: rotate(180deg);
}
</style>
