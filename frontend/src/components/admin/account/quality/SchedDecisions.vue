<template>
  <section class="qp-section" data-qp-section="decisions">
    <h3 class="qp-h">{{ t('admin.schedQuality.decisions.title') }}</h3>
    <div class="dec" :aria-busy="state === 'loading'">
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
          <li v-for="entry in visible" :key="entry.key" class="drow" :data-qp-decision="entry.items[0].id">
            <span class="drow__time">{{ formatShortTime(entry.at) }}</span>
            <!-- Everything after the time, so the lines under the action start where it starts. -->
            <div class="drow__body">
              <div class="drow__main">
                <span class="drow__what" :class="`drow__what--${tone(entry)}`">{{ actionWord(entry) }}</span>
                <span v-if="endText(entry)" class="drow__end">{{ endText(entry) }}</span>
                <button
                  v-if="entry.items.some((item) => item.basis)"
                  type="button"
                  class="qp-text-btn drow__ev"
                  :aria-expanded="shown.has(entry.key)"
                  :data-qp-decision-evidence="entry.items[0].id"
                  @click="toggleEvidence(entry.key)"
                >{{ t('admin.schedQuality.decisions.evidence') }}</button>
              </div>
              <p class="drow__where">{{ where(entry) }}</p>
              <p v-if="entry.line && entry.action === 'trial_timeout'" class="drow__sub" :data-qp-trial-timeout="entry.items[0].id">{{ timeoutReason(entry.items[0]) }}</p>
              <template v-if="shown.has(entry.key)">
                <div v-for="item in entry.items.filter((one) => one.basis)" :key="item.id" class="drow__basis">
                  <p v-if="entry.groups.length > 1" class="drow__group">{{ groupLabel(item.group_id) }}</p>
                  <SchedEvidence
                    :dim="item.basis!.line"
                    :evidence="basisEvidence(item.basis!)"
                    :samples="item.basis"
                    :source-groups="item.basis!.source_groups"
                    :group-name="groupName"
                  />
                </div>
              </template>
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
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * The quality panel's "判定记录": each change of a line's identity for the account (GET …/accounts/:id/decisions),
 * newest first, 50 at a time, loaded with the panel. Records of one moment, action and model's line across groups read
 * as one entry naming the groups (mergeDecisions); the newest six show, "查看更多" shows the rest and then loads older
 * pages. Another account starts over, and a quality change made from the page (a recovery, an exemption) loads the
 * first page again. An entry of the new rules opens to the evidence each group's record was taken on; records of the
 * previous rules have none and read "旧规则 · <action>".
 */
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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
/** Rear stay of each level, minutes. */
const STAY_MINUTES = [20, 40, 60, 90, 120]

const props = defineProps<{
  accountId: number
  /** A group's name by id. */
  groupName: (id: number) => string | undefined
}>()
const { t, te } = useI18n()
const { changed } = useSchedLayers()

const state = ref<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
const items = ref<BackendDecision[]>([])
const more = ref(false)
const all = ref(false)
const shown = reactive(new Set<string>())
let seq = 0

const merged = computed(() => mergeDecisions(items.value))
const visible = computed(() => (all.value ? merged.value : merged.value.slice(0, FIRST)))
/** More to show: entries held back, then older pages. */
const canMore = computed(() => (!all.value && merged.value.length > FIRST) || more.value)

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
  if (!all.value && merged.value.length > FIRST) all.value = true
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

function toggleEvidence(key: string) {
  if (shown.has(key)) shown.delete(key)
  else shown.add(key)
}

/** The action in words (其他 for one the copy does not know); records of the previous rules (no line) say so first. */
function actionWord(entry: MergedDecision): string {
  const word = schedWord('decisions.action', entry.action, t, te)
  return entry.line ? word : t('admin.schedQuality.decisions.legacy', { action: word })
}

const BAD = new Set(['enter', 'renew', 'trial_fail'])
const GOOD = new Set(['release', 'trial_pass', 'clear', 'relax', 'reset'])
const tone = (entry: MergedDecision) => (!entry.line ? 'muted' : BAD.has(entry.action) ? 'rear' : GOOD.has(entry.action) ? 'pass' : entry.action === 'trial' ? 'trial' : 'muted')

const groupLabel = (id: number) => props.groupName(id) ?? `#${id}`

function where(entry: MergedDecision): string {
  const line = entry.line ? t(`admin.schedQuality.dim.${entry.line}`) : ''
  const groups = entry.groups.map(groupLabel).join(t('admin.schedQuality.evidence.joiner'))
  return [entry.model, line, groups].filter(Boolean).join(' · ')
}

/** A rear stay's length and end, or a trial's end. */
function endText(entry: MergedDecision): string {
  if (!entry.line || !entry.until) return ''
  if (entry.action === 'trial') return t('admin.schedQuality.decisions.trialUntil', { time: formatShortTime(entry.until) })
  if (BAD.has(entry.action)) {
    const minutes = STAY_MINUTES[Math.min(Math.max(entry.level, 0), STAY_MINUTES.length - 1)]
    return t('admin.schedQuality.decisions.rearUntil', { time: formatShortTime(entry.until), minutes })
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
@reference "../../../../style.css";

/* The panel's heading rule is scoped to the panel and does not reach this component's heading. */
.qp-h {
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-body);
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
.dlist {
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
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 8px;
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
  color: var(--color-status-error-text);
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
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
/* Evidence at the end of the line (the panel's text-button rule sets its margins: this one outranks it). */
.drow .drow__main .drow__ev {
  flex: none;
  margin-left: auto;
}
.drow__where,
.drow__sub,
.drow__group {
  margin: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.drow__group {
  margin-top: 4px;
  color: var(--color-text-primary);
}
/* An opened evidence wraps anywhere: one long word in it (a source group's name) would otherwise widen the list. */
.drow__basis {
  min-width: 0;
  overflow-wrap: anywhere;
}
</style>
