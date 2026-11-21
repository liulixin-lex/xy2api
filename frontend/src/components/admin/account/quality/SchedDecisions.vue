<template>
  <section class="dec" data-qp-section="decisions" :aria-label="t('admin.schedQuality.decisions.title')" :aria-busy="state === 'loading'">
    <div v-if="state === 'failed' && !items.length" class="dec__failed" data-qp-decisions-failed>
      <span class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="qp-text-btn" @click="load(false)">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <div v-else-if="state !== 'loaded' && state !== 'failed' && !items.length" class="dec__loading">
      <span v-for="i in 3" :key="i" class="skeleton block h-3.5 w-full" />
    </div>
    <p v-else-if="!items.length" class="qp-note" data-qp-decisions-empty>{{ t('admin.schedQuality.decisions.empty') }}</p>
    <template v-else>
      <ol class="dlist">
        <li v-for="entry in visible" :key="entry.key" class="drow" :class="{ 'is-open': shown.has(entry.key) }" :data-qp-decision="entry.items[0].id">
          <component
            :is="entry.expandable ? 'button' : 'div'"
            :type="entry.expandable ? 'button' : undefined"
            class="drow__line"
            :aria-expanded="entry.expandable ? shown.has(entry.key) : undefined"
            :aria-controls="entry.expandable ? `${idPrefix}-${entry.items[0].id}` : undefined"
            :data-qp-decision-evidence="entry.expandable ? entry.items[0].id : undefined"
            @click="entry.expandable && toggle(entry.key)"
          >
            <span class="drow__time">{{ formatShortTime(entry.at) }}</span>
            <span class="drow__what" :class="`drow__what--${tone(entry)}`">{{ actionWord(entry) }}</span>
            <span v-if="entry.end" class="drow__end" data-qp-decision-end>
              <Icon name="arrowRight" size="xs" :stroke-width="2.25" /><span class="sr-only">{{ t('admin.schedQuality.decisions.until') }}</span>{{ entry.end }}
            </span>
            <span v-if="entry.line" class="drow__dim">{{ t(`admin.schedQuality.dim.${entry.line}`) }}</span>
            <span class="drow__meta">
              <span class="drow__model" :title="entry.model">{{ entry.model }}</span>
              <span class="drow__groups" :title="entry.groups.map((id) => group(id).name).join(t('admin.schedQuality.evidence.joiner'))">
                <GroupAliasPill v-for="id in entry.groups.slice(0, PILLS)" :key="id" :group="group(id)" />
                <span v-if="entry.groups.length > PILLS" class="drow__more-groups" aria-hidden="true">{{ t('admin.schedQuality.more', { n: entry.groups.length - PILLS }) }}</span>
                <span class="sr-only">{{ entry.groups.map((id) => group(id).name).join(t('admin.schedQuality.evidence.joiner')) }}</span>
              </span>
            </span>
            <Icon v-if="entry.expandable" name="chevronRight" size="xs" :stroke-width="2.5" class="drow__caret" />
          </component>
          <!-- The opened record: each group's evidence (and why a trial ended without a verdict); it grows open. -->
          <div
            v-if="entry.expandable"
            :id="`${idPrefix}-${entry.items[0].id}`"
            class="drow__open"
            :inert="!shown.has(entry.key) || undefined"
          >
            <div class="drow__open-inner">
              <div v-for="item in entry.items" :key="item.id" class="drow__basis" :data-qp-decision-item="item.id">
                <p v-if="entry.items.length > 1" class="drow__group">
                  {{ group(item.group_id).name }}<span v-if="entry.endsDiffer && item.until" class="drow__group-end"> → {{ exactTime(item.until) }}</span>
                </p>
                <p v-if="item.line && item.action === 'trial_timeout'" class="drow__sub" :data-qp-trial-timeout="item.id">{{ timeoutReason(item) }}</p>
                <SchedEvidence
                  v-if="item.basis"
                  :dim="item.basis.line"
                  :evidence="basisEvidence(item.basis)"
                  :samples="item.basis"
                  :source-groups="item.basis.source_groups"
                  :group-name="(id) => group(id).name"
                />
              </div>
            </div>
          </div>
        </li>
      </ol>
      <div v-if="canMore || state === 'failed'" class="dec__more">
        <span v-if="state === 'failed'" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
        <button type="button" class="qp-text-btn" :disabled="state === 'loading'" data-qp-decisions-more @click="onMore">
          {{ t('admin.schedQuality.decisions.more') }}
        </button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
/**
 * The quality panel's decision records (GET …/accounts/:id/decisions), newest first, 50 at a time, loaded with the
 * panel. One entry a line: the time, the action in its colour, where it ends ("→ 10:04"), the line, the model and the
 * groups as their pills. Records of one minute, action, model, line and end minute across groups are one entry
 * (mergeDecisions); the newest six show, "查看更多" shows the rest and then loads older pages. A line with evidence is
 * a button: it grows open to each record's evidence, its own end when the records' ends differ, and why a trial ended
 * without a verdict. Another account starts over; a quality change made from the page loads the first page again.
 * Records of the previous rules have no evidence and read "旧规则 · <action>".
 */
import { computed, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendBasis, BackendDecision } from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import { schedWord } from '@/utils/schedQualityFormat'
import SchedEvidence from './SchedEvidence.vue'
import { evidenceCauses, mergeDecisions, type MergedDecision } from './qualityPanel'

const PAGE = 50
/** Entries shown before "查看更多". */
const FIRST = 6
/** Group pills on a line before "+N". */
const PILLS = 3

const props = defineProps<{
  accountId: number
  /** A group's name and alias by id. */
  group: (id: number) => { name: string; alias?: string | null }
}>()
const { t, te } = useI18n()
const { changed } = useSchedLayers()
const idPrefix = `qp-dec-${useId()}`

const state = ref<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
const items = ref<BackendDecision[]>([])
const more = ref(false)
const all = ref(false)
const shown = reactive(new Set<string>())
let seq = 0

const BAD = new Set(['enter', 'renew', 'trial_fail'])
const GOOD = new Set(['release', 'trial_pass', 'clear', 'relax', 'reset'])

/** Where an entry ends: a rear stay's or a trial's end, as shown. */
const endOf = (entry: MergedDecision) =>
  entry.line && entry.until && (entry.action === 'trial' || BAD.has(entry.action)) ? formatShortTime(entry.until) : ''

const entries = computed(() =>
  mergeDecisions(items.value).map((entry) => ({
    ...entry,
    end: endOf(entry),
    /** The records' own ends differ within the minute shown: each says its own when opened. */
    endsDiffer: new Set(entry.items.map((item) => item.until)).size > 1,
    expandable: entry.items.some((item) => item.basis || (item.line && item.action === 'trial_timeout'))
  }))
)
const visible = computed(() => (all.value ? entries.value : entries.value.slice(0, FIRST)))
/** More to show: entries held back, then older pages. */
const canMore = computed(() => (!all.value && entries.value.length > FIRST) || more.value)

async function load(next: boolean) {
  const accountId = props.accountId
  const request = ++seq
  state.value = 'loading'
  try {
    const before = next ? items.value[items.value.length - 1]?.id : undefined
    const page = await schedQualityAPI.accountDecisions(accountId, { limit: PAGE, beforeId: before })
    if (request !== seq) return
    items.value = next ? [...items.value, ...page] : page
    more.value = page.length >= PAGE
    state.value = 'loaded'
  } catch {
    if (request !== seq) return
    state.value = 'failed'
    // The first page did not come: what was shown before is out of date, and its last id must not page on from there
    // (records written since would be skipped). The retry then asks for the first page again.
    if (!next) {
      items.value = []
      more.value = false
    }
  }
}

function onMore() {
  if (!all.value && entries.value.length > FIRST) all.value = true
  else {
    all.value = true
    void load(true)
  }
}

watch(() => props.accountId, () => {
  seq++
  items.value = []
  more.value = false
  all.value = false
  shown.clear()
  void load(false)
}, { immediate: true })

// A recovery or an exemption writes records: the list starts over from the newest.
watch(changed, () => {
  if (state.value === 'idle') return
  all.value = false
  trackSchedReload(load(false))
})

function toggle(key: string) {
  if (shown.has(key)) shown.delete(key)
  else shown.add(key)
}

/** The action in words (其他 for one the copy does not know); records of the previous rules (no line) say so first. */
function actionWord(entry: MergedDecision): string {
  const word = schedWord('decisions.action', entry.action, t, te)
  return entry.line ? word : t('admin.schedQuality.decisions.legacy', { action: word })
}

const tone = (entry: MergedDecision) => (!entry.line ? 'muted' : BAD.has(entry.action) ? 'rear' : GOOD.has(entry.action) ? 'pass' : entry.action === 'trial' ? 'trial' : 'muted')

/** A record's own end to the second, for records whose ends share the minute shown. */
function exactTime(value: string): string {
  const date = new Date(value)
  return `${formatShortTime(value)}:${String(date.getSeconds()).padStart(2, '0')}`
}

const basisEvidence = (basis: BackendBasis) => basis.ttft ?? basis.error ?? basis.cache ?? null

/**
 * Why a trial ended without a verdict, from the basis as it is (the backend gives no reason code): no traffic of this
 * group during the trial, or not enough evidence (the first-output line adds rule B's reasons); then, on its own, what
 * was seen of the trial seats. Each part is read on its own: one the record does not have says so without hiding the
 * other, and a record with neither is an older decision. Nothing is worked out.
 */
function timeoutReason(item: BackendDecision): string {
  const basis = item.basis
  const local = basis?.samples_local
  const seats = basis?.trial_seats
  if (!basis || (local == null && seats == null)) {
    return t('admin.schedQuality.decisions.timeout.line', { reasons: t('admin.schedQuality.decisions.timeout.legacy') })
  }
  const parts: string[] = []
  if (local == null) parts.push(t('admin.schedQuality.decisions.timeout.trafficLegacy'))
  else if (local === 0) parts.push(t('admin.schedQuality.decisions.timeout.noTraffic'))
  else {
    const causes = evidenceCauses(basis.line, basis.ttft).map((cause) => {
      const key = `admin.schedQuality.evidence.causes.${cause}`
      return te(key) ? t(key) : t('admin.schedQuality.unknown')
    })
    parts.push(causes.length
      ? t('admin.schedQuality.decisions.timeout.evidenceWhy', { reasons: causes.join(t('admin.schedQuality.evidence.joiner')) })
      : t('admin.schedQuality.decisions.timeout.evidence'))
  }
  parts.push(seats == null ? t('admin.schedQuality.decisions.timeout.seatsLegacy') : t(`admin.schedQuality.decisions.timeout.seats.${seats}`))
  return t('admin.schedQuality.decisions.timeout.line', { reasons: parts.join(t('admin.schedQuality.decisions.timeout.joiner')) })
}
</script>

<style scoped>
@reference "../../../../style.css";

/* Set apart from the table above by a rule and room, without a title. */
.dec {
  display: grid;
  gap: 6px;
  padding-top: 14px;
  border-top: 1px solid var(--color-line-row);
}
.dec__loading {
  display: grid;
  gap: 10px;
}
.dec__failed,
.dec__more {
  display: flex;
  align-items: center;
  gap: 10px;
}
.dlist {
  display: grid;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}
/* One record a line; the whole line is the control when it has evidence. */
.drow__line {
  display: flex;
  width: calc(100% + 16px);
  min-height: 34px;
  align-items: center;
  gap: 8px;
  margin: 0 -8px;
  padding: 4px 8px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
}
button.drow__line {
  cursor: pointer;
  transition: background-color var(--motion-fast) var(--ease-out);
}
@media (hover: hover) {
  button.drow__line:hover {
    background: var(--color-bg-control-hover);
  }
  button.drow__line:hover .drow__caret {
    color: var(--color-text-primary);
  }
}
button.drow__line:active {
  background: var(--color-bg-seg-active);
}
button.drow__line:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
.drow.is-open button.drow__line {
  background: var(--color-bg-control-hover);
}
.drow__time {
  min-width: 38px;
  flex: none;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
.drow__what {
  flex: none;
  font-weight: var(--font-weight-medium);
}
/* One set of colours with the table: amber rear, green pass, blue trial. */
.drow__what--rear {
  color: var(--color-status-back-text);
}
.drow__what--pass {
  color: var(--color-status-ok-text);
}
.drow__what--trial {
  color: var(--color-status-info-text);
}
.drow__what--muted {
  color: var(--color-text-muted);
}
.drow__end {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 2px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
.drow__dim {
  flex: none;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
/* The model and the groups: on the line, or under the action on a phone. */
.drow__meta {
  display: contents;
}
.drow__model {
  max-width: 128px;
  flex: none;
  overflow: hidden;
  padding: 0 6px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-size: 11px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.drow__groups {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: center;
  gap: 4px;
  overflow: hidden;
}
.drow__more-groups {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.drow__caret {
  flex: none;
  color: var(--color-text-muted);
  transition: transform var(--motion-base) var(--ease-out), color var(--motion-fast) var(--ease-out);
}
.drow.is-open .drow__caret {
  transform: rotate(90deg);
}
/* Grows open from nothing (rows 0fr → 1fr), and is out of reach while closed. */
.drow__open {
  display: grid;
  grid-template-rows: 0fr;
  visibility: hidden;
  transition: grid-template-rows var(--motion-base) var(--ease-out), visibility 0s linear var(--motion-base);
}
.drow.is-open .drow__open {
  grid-template-rows: 1fr;
  visibility: visible;
  transition: grid-template-rows var(--motion-base) var(--ease-out), visibility 0s;
}
.drow__open-inner {
  display: grid;
  min-height: 0;
  gap: 8px;
  overflow: hidden;
  padding-left: 46px;
}
.drow.is-open .drow__open-inner {
  padding-top: 6px;
  padding-bottom: 10px;
}
.drow__basis {
  display: grid;
  min-width: 0;
  gap: 4px;
  overflow-wrap: anywhere;
}
.drow__group {
  margin: 0;
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
}
.drow__group-end {
  color: var(--color-text-secondary);
  font-weight: var(--font-weight-regular);
  font-variant-numeric: tabular-nums;
}
.drow__sub {
  margin: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
@media (prefers-reduced-motion: reduce) {
  .drow__open,
  .drow.is-open .drow__open,
  .drow__caret {
    transition: none;
  }
}
/* Touch: every line at least 40px high. */
@media (pointer: coarse) {
  .drow__line {
    min-height: 40px;
  }
}
/* A phone: the model and the groups take a line of their own under the action; the arrow stays on the first. */
@media (max-width: 439px) {
  .drow__line {
    flex-wrap: wrap;
    row-gap: 4px;
  }
  .drow__meta {
    display: flex;
    min-width: 0;
    flex-basis: 100%;
    order: 1;
    align-items: center;
    gap: 8px;
    padding-left: 46px;
  }
  .drow__caret {
    margin-left: auto;
  }
}
</style>
