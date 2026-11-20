<template>
  <section class="qp-section" data-qp-section="decisions">
    <h3 class="qp-h">
      <button
        type="button"
        class="dec-toggle"
        :aria-expanded="open"
        :aria-controls="listId"
        data-qp-decisions-toggle
        @click="toggle"
      >
        <Icon name="chevronRight" size="xs" :stroke-width="2.5" />
        <span>{{ t('admin.schedQuality.decisions.title') }}</span>
      </button>{{ ' ' }}<span class="qp-h-note" data-qp-decisions-note>{{ t('admin.schedQuality.decisions.note') }}</span>
    </h3>
    <div v-if="open" :id="listId" class="dec" :aria-busy="state === 'loading'">
      <div v-if="state === 'failed' && !items.length" class="dec__failed" data-qp-decisions-failed>
        <span class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
        <button type="button" class="qp-text-btn" @click="load(false)">{{ t('admin.schedQuality.action.retry') }}</button>
      </div>
      <div v-else-if="state === 'loading' && !items.length" class="dec__loading">
        <span v-for="i in 3" :key="i" class="skeleton block h-3.5 w-full" />
      </div>
      <p v-else-if="!items.length" class="qp-note" data-qp-decisions-empty>{{ t('admin.schedQuality.decisions.empty') }}</p>
      <template v-else>
        <ol class="dlist">
          <li v-for="item in items" :key="item.id" class="drow" :data-qp-decision="item.id">
            <span class="drow__time">{{ formatShortTime(item.at) }}</span>
            <!-- Everything after the time, so the lines under the action start where it starts (design 6.5). -->
            <div class="drow__body">
              <div class="drow__main">
                <span class="drow__what" :class="`drow__what--${tone(item)}`">{{ actionWord(item) }}</span>
                <span class="drow__where">{{ where(item) }}</span>
                <button
                  v-if="item.basis"
                  type="button"
                  class="qp-text-btn drow__ev"
                  :aria-expanded="shown.has(item.id)"
                  :data-qp-decision-evidence="item.id"
                  @click="toggleEvidence(item.id)"
                >{{ t('admin.schedQuality.decisions.evidence') }}</button>
              </div>
              <p v-if="detailLine(item)" class="drow__sub">{{ detailLine(item) }}</p>
              <p v-if="item.line && item.action === 'trial_timeout'" class="drow__sub" :data-qp-trial-timeout="item.id">{{ timeoutReason(item) }}</p>
              <SchedEvidence
                v-if="item.basis && shown.has(item.id)"
                class="drow__basis"
                :dim="item.basis.line"
                :evidence="basisEvidence(item.basis)"
                :samples="item.basis"
                :source-groups="item.basis.source_groups"
                :group-name="groupName"
              />
            </div>
          </li>
        </ol>
        <div v-if="more || state === 'failed'" class="dec__more">
          <span v-if="state === 'failed'" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
          <button type="button" class="qp-text-btn" :disabled="state === 'loading'" data-qp-decisions-more @click="load(true)">
            {{ t('admin.schedQuality.decisions.more') }}
          </button>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * The quality panel's "判定记录": each change of a line's identity for the account (GET …/accounts/:id/decisions),
 * newest first, 50 at a time. Closed by default; the first opening loads it, another account starts over, and a
 * quality change made from the page (a recovery, an exemption) loads the first page again. A record of the new rules
 * opens to the evidence it was taken on; records of the previous rules have none and read "旧规则 · <action>".
 */
import { reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { BackendBasis, BackendDecision } from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import { schedWord } from '@/utils/schedQualityFormat'
import SchedEvidence from './SchedEvidence.vue'
import { evidenceCauses } from './qualityPanel'

const PAGE = 50
/** Rear stay of each level, minutes. */
const STAY_MINUTES = [20, 40, 60, 90, 120]

const props = defineProps<{
  accountId: number
  /** A group's name by id. */
  groupName: (id: number) => string | undefined
}>()
const { t, te } = useI18n()
const { changed } = useSchedLayers()
const listId = `qp-decisions-${useId()}`

const open = ref(false)
const state = ref<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
const items = ref<BackendDecision[]>([])
const more = ref(false)
const shown = reactive(new Set<number>())
let seq = 0

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

function toggle() {
  open.value = !open.value
  if (open.value && state.value === 'idle') void load(false)
}

watch(() => props.accountId, () => {
  seq++
  state.value = 'idle'
  items.value = []
  more.value = false
  shown.clear()
  if (open.value) void load(false)
})

// A recovery or an exemption writes records: an open list starts over from the newest; a collapsed one is out of date,
// so its next opening does (an answer still on its way is dropped).
watch(changed, () => {
  if (state.value === 'idle') return
  if (open.value) {
    trackSchedReload(load(false))
    return
  }
  seq++
  state.value = 'idle'
})

function toggleEvidence(id: number) {
  if (shown.has(id)) shown.delete(id)
  else shown.add(id)
}

/** The action in words (其他 for one the copy does not know); records of the previous rules (no line) say so first. */
function actionWord(item: BackendDecision): string {
  const word = schedWord('decisions.action', item.action, t, te)
  return item.line ? word : t('admin.schedQuality.decisions.legacy', { action: word })
}

const BAD = new Set(['enter', 'renew', 'trial_fail'])
const GOOD = new Set(['release', 'trial_pass', 'clear', 'relax', 'reset'])
const tone = (item: BackendDecision) => (!item.line ? 'muted' : BAD.has(item.action) ? 'rear' : GOOD.has(item.action) ? 'pass' : item.action === 'trial' ? 'trial' : 'muted')

function where(item: BackendDecision): string {
  const group = props.groupName(item.group_id) ?? `#${item.group_id}`
  const line = item.line ? t(`admin.schedQuality.dim.${item.line}`) : ''
  return [group, item.model, line].filter(Boolean).join(' · ')
}

/** A rear stay's length and end, or a trial's end. */
function detailLine(item: BackendDecision): string {
  if (!item.line || !item.until) return ''
  if (item.action === 'trial') return t('admin.schedQuality.decisions.trialUntil', { time: formatShortTime(item.until) })
  if (BAD.has(item.action)) {
    const minutes = STAY_MINUTES[Math.min(Math.max(item.level, 0), STAY_MINUTES.length - 1)]
    return t('admin.schedQuality.identity.rearUntil', { time: formatShortTime(item.until), minutes })
  }
  return ''
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
/* The panel's heading rule is scoped to the panel and does not reach this component's heading: a heading layout here,
   so the title and the note stand apart (they ran together in English: "DecisionsOne record …"). The note is never
   cut: it wraps, and on a narrow drawer (390 or 320 px) it moves under the title. */
.qp-h {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 4px 8px;
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-body);
}
.qp-h-note {
  min-width: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-regular);
}
.dec-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin: 0 -4px;
  padding: 0 4px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: inherit;
  font: inherit;
}
.dec-toggle svg {
  color: var(--color-text-secondary);
  transition: transform var(--motion-fast) ease-out;
}
.dec-toggle[aria-expanded='true'] svg {
  transform: rotate(90deg);
}
.dec-toggle:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
@media (prefers-reduced-motion: reduce) {
  .dec-toggle svg {
    transition: none;
  }
}
.dec {
  display: grid;
  gap: 8px;
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
/* The list is the container the rows lay out by: the drawer's width is not the viewport's. */
.dlist {
  container: sched-decisions / inline-size;
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}
/* The time, then the rest of the record beside it: the action's line, and under the action its detail lines and opened
   evidence. */
.drow {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: baseline;
  column-gap: 8px;
  padding: 8px 0;
  border-top: 1px solid var(--color-line-row);
}
.drow__body {
  display: grid;
  min-width: 0;
  gap: 4px;
}
.drow:first-child {
  border-top: 0;
}
.drow__main {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: 8px;
}
.drow__time {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
/* The action gives way last (after the group text) and wraps when even then the line is too short: a classic vertical
   scrollbar makes the list 8–15px narrower than measured, and an older record's English action is the longest text. */
.drow__what {
  flex: 0 1 auto;
  min-width: 0;
  overflow-wrap: anywhere;
  font-weight: var(--font-weight-medium);
}
.drow__what--rear {
  color: var(--color-mark-fail);
}
.drow__what--pass {
  color: var(--color-mark-pass);
}
.drow__what--trial {
  color: var(--color-status-info);
}
.drow__what--muted {
  color: var(--color-text-muted);
}
.drow__where {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.drow__ev {
  flex: none;
}
/* A narrow list: one line cannot hold the time, the longest action and Evidence, and nothing in it gave way, so
   Evidence ended up out of view. Here the action (wrapping, long words too) and Evidence share the first line beside
   the time, Evidence keeping its whole width; the group, model and line go under the action. 454px is the widest list
   measured to need it (English, a record of another day: "11-18 06:55 Not enough evidence at the review, trial
   started", Evidence); the 500px drawer from 1280px (a 459px list) keeps the one-line row. */
@container sched-decisions (max-width: 454px) {
  .drow__main {
    display: grid;
    grid-template-columns: minmax(0, 1fr) max-content;
    grid-template-areas:
      'what ev'
      'where where';
    row-gap: 2px;
  }
  .drow__what {
    grid-area: what;
    overflow-wrap: anywhere;
  }
  .drow__where {
    grid-area: where;
    overflow-wrap: anywhere;
    white-space: normal;
  }
  .drow__ev {
    grid-area: ev;
  }
}
/* An opened evidence wraps anywhere: one long word in it (a source group's name) would otherwise widen the list's
   column and take every row's Evidence out of view. */
.drow__basis {
  min-width: 0;
  overflow-wrap: anywhere;
}
.drow__sub {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
</style>
