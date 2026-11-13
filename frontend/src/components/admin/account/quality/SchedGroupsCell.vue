<template>
  <span v-if="!openai" class="sq-text-3 text-sm">—</span>
  <span v-else-if="loading" class="skeleton inline-block h-3.5 w-10 align-middle" />
  <span v-else-if="!summary" />
  <span v-else class="flex flex-nowrap items-center gap-1.5 max-md:flex-wrap max-md:justify-end" data-sq-groups>
    <template v-if="capsules.length">
      <button
        v-for="item in capsules.slice(0, MAX_SHOWN)"
        :key="item.key"
        type="button"
        v-tip="tipOf(item)"
        :class="['sq-capsule sq-focus', CLASS[item.kind], { 'sq-capsule--all': item.groupId === 0, 'sq-capsule--new': isNew(item) }]"
        :aria-label="ariaOf(item)"
        :data-sq-trigger="`cap:${summary.account_id}:${item.groupId}`"
        :data-sq-kind="item.kind"
        @click.stop="open(item)"
      >
        <svg v-if="item.kind === 'exempt'" class="sq-capsule__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l7 3v5c0 4.4-2.9 8.3-7 10-4.1-1.7-7-5.6-7-10V6z" /></svg>
        <span :ref="(el) => measure(item.key, el)" class="sq-capsule__name">{{ nameOf(item) }}</span> <span class="sq-capsule__state">{{ t(STATE_KEY[item.kind]) }}</span>
      </button>
      <button
        v-if="capsules.length > MAX_SHOWN"
        type="button"
        class="sq-plain sq-focus sq-text-3 text-xs"
        :data-sq-trigger="`grp:${summary.account_id}`"
        @click.stop="emit('open', { model: summary.model, toGroups: true })"
      >{{ t('admin.schedQuality.more', { n: capsules.length - MAX_SHOWN }) }}</button>
    </template>
    <button
      v-else-if="summary.groups.total === summary.groups.nodata"
      type="button"
      class="sq-plain sq-focus sq-text-3 text-sm"
      :data-sq-trigger="`grp:${summary.account_id}`"
      @click.stop="emit('open', { model: summary.model, toGroups: true })"
    >{{ t('admin.schedQuality.state.nodata') }}</button>
    <button
      v-else
      type="button"
      class="sq-plain sq-focus inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300"
      :data-sq-trigger="`grp:${summary.account_id}`"
      @click.stop="emit('open', { model: summary.model, toGroups: true })"
    ><span class="sq-dot" aria-hidden="true" />{{ t('admin.schedQuality.state.normal') }}</button>
  </span>
</template>

<script setup lang="ts">
import { computed, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary } from '@/types/schedQuality'
import { vTip } from '@/utils/tip'

// Groups column of the account list (ui-proposal section 4, contract delta E): one capsule per group, the group's own
// override winning over its cooling; the all-groups override is one capsule that stands for every group without its
// own override. Order: all groups, own overrides, cooling; at most 3, then +N (N = candidates - 3).
// Otherwise `正常` with a dot or `暂无数据`; non-OpenAI accounts show a dash. Kept on one line for the virtualizer.
const props = defineProps<{
  summary?: SchedAccountSummary
  loading?: boolean
  /** Only OpenAI accounts are under the quality gate. */
  openai?: boolean
  groupName: (id: number) => string
  /** Capsule keys (`account:group:state`) that newly appeared on this refresh and get the one-time emphasis. */
  fresh?: Set<string>
}>()
const emit = defineEmits<{ open: [target: { model: string | null; groupId?: number; toGroups?: boolean }] }>()
const { t } = useI18n()

const MAX_SHOWN = 3
type Kind = 'exempt' | 'cooling'
const STATE_KEY: Record<Kind, string> = {
  exempt: 'admin.schedQuality.state.exempted',
  cooling: 'admin.schedQuality.state.cooling'
}
const RANK: Record<Kind, number> = { exempt: 1, cooling: 2 }
const CLASS: Record<Kind, string> = { exempt: 'sq-capsule--manual', cooling: 'sq-capsule--cool' }

interface Capsule {
  key: string
  /** 0 = all groups. */
  groupId: number
  kind: Kind
  model: string | null
  expiresAt: string | null
}

const capsules = computed<Capsule[]>(() => {
  const s = props.summary
  if (!s) return []
  const items: Capsule[] = []
  let all = false
  const own = new Set<number>()
  for (const o of s.overrides) {
    if (o.group_id === 0) all = true
    else own.add(o.group_id)
    items.push({ key: `o:${o.group_id}`, groupId: o.group_id, kind: o.mode, model: s.model, expiresAt: o.expires_at })
  }
  // Cooling under any override is not shown here (the drawer has it).
  if (!all) {
    for (const c of s.cooling) {
      if (own.has(c.group_id)) continue
      items.push({ key: `c:${c.group_id}`, groupId: c.group_id, kind: 'cooling', model: c.model, expiresAt: null })
    }
  }
  return items.sort((a, b) => (a.groupId === 0 ? -1 : b.groupId === 0 ? 1 : RANK[a.kind] - RANK[b.kind] || a.groupId - b.groupId))
})

const nameOf = (item: Capsule) => (item.groupId === 0 ? t('admin.schedQuality.override.allGroups') : props.groupName(item.groupId))
const manual = (item: Capsule) => item.kind === 'exempt'

// Expiry of an override: `至 18:30` today, `至 11-14 18:30` later, `一直` without one.
const pad = (n: number) => String(n).padStart(2, '0')
function until(at: string | null): string {
  if (!at) return t('admin.schedQuality.override.forever')
  const d = new Date(at)
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const today = new Date()
  const sameDay = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate()
  return t('admin.schedQuality.tip.until', { time: sameDay ? time : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}` })
}

const tipOf = (item: Capsule) => {
  const name = item.groupId !== 0 && truncated[item.key] ? nameOf(item) : ''
  if (!manual(item)) return name
  return name ? `${name} ${until(item.expiresAt)}` : until(item.expiresAt)
}
const ariaOf = (item: Capsule) =>
  [nameOf(item), t(STATE_KEY[item.kind]), ...(manual(item) ? [until(item.expiresAt)] : [])].join(' ')

// The all-groups capsule opens the drawer at its top; a group capsule scrolls to that group's row.
function open(item: Capsule) {
  if (item.groupId === 0) emit('open', { model: item.model })
  else emit('open', { model: item.model, groupId: item.groupId })
}

const isNew = (item: Capsule) =>
  !manual(item) && !!props.fresh?.has(`${props.summary?.account_id}:${item.groupId}:${item.kind}`)

// A capsule whose group name is cut off shows the full name in its tip.
const truncated = reactive<Record<string, boolean>>({})
function measure(key: string, el: unknown) {
  if (!(el instanceof HTMLElement)) return
  requestAnimationFrame(() => {
    const cut = el.scrollWidth > el.clientWidth
    if (truncated[key] !== cut) truncated[key] = cut
  })
}
</script>
