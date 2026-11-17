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
      </button>
      <span class="qp-h-note">{{ t('admin.schedQuality.decisions.note') }}</span>
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
            <div class="drow__main">
              <span class="drow__time">{{ formatShortTime(item.at) }}</span>
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
            <SchedEvidence
              v-if="item.basis && shown.has(item.id)"
              class="drow__basis"
              :dim="item.basis.line"
              :evidence="basisEvidence(item.basis)"
              :source-groups="item.basis.source_groups"
              :group-name="groupName"
            />
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
    const before = next ? items.value.at(-1)?.id : undefined
    const page = await schedQualityAPI.accountDecisions(accountId, { limit: PAGE, beforeId: before })
    if (request !== seq) return
    items.value = next ? [...items.value, ...page] : page
    more.value = page.length >= PAGE
    state.value = 'loaded'
  } catch {
    if (request === seq) state.value = 'failed'
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
</script>

<style scoped>
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
.dlist {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}
.drow {
  display: grid;
  gap: 4px;
  padding: 8px 0;
  border-top: 1px solid var(--color-line-row);
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
  flex: none;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
.drow__what {
  flex: none;
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
.drow__sub {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
}
</style>
