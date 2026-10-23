import { computed, ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import { DEFAULT_REFRESH_SECONDS, MASTER_KEY } from './catalog'
import { isNotFound, policyApi, toWriteFailure, type PolicyApi } from './api'
import { applyChoices, changedFields, cloneValue, inlineIssues, isAvailable, mergeDraft, partialSpec, setPath } from './schema'
import type {
  AvailablePolicy,
  HistoryResponse,
  PoliciesResponse,
  PolicyConflict,
  PolicyEntry,
  PolicySpec,
  ValidateResult,
  WriteFailure,
} from './types'

export interface PreviewState {
  key: string
  /** Latest entry the preview was computed against (its rev is the expected_rev). */
  entry: AvailablePolicy
  spec: PolicySpec
  validating: boolean
  result: ValidateResult | null
  validateFailure: WriteFailure | null
  submitting: boolean
  failure: WriteFailure | null
  /** Set when someone else wrote the key after the admin started editing (M-3). */
  conflict: PolicyConflict | null
  /** Merged draft waiting for the per-field choices; null once committed. */
  pendingDraft: PolicySpec | null
  /** The merge or the admin's choices left nothing to write. */
  nothingLeft: boolean
}

export interface WriteReceipt {
  rev: number
  at: Date
  effectiveBy: Date
  /** The deadline passed and the panel re-read the server state. */
  settled: boolean
  /** Written under the static kill switch: saved only, nothing takes effect until it is lifted (FE-Q4). */
  savedOnly: boolean
}

export interface PolicyDeps {
  api: PolicyApi
  loadGroups: () => Promise<AdminGroup[]>
  now: () => Date
}

const defaultDeps: PolicyDeps = {
  api: policyApi,
  loadGroups: () => adminAPI.groups.getAllIncludingInactive(),
  now: () => new Date(),
}

/** Automatic re-merges when validate keeps answering `rev_stale` (someone writing in a loop). */
const MAX_STALE_RETRIES = 2
/** Re-reads of a pending_reload key, one refresh period apart, before the panel leaves it to the reload button (n-new-2). */
const PENDING_REREADS = 3

export function usePolicies(overrides: Partial<PolicyDeps> = {}) {
  const deps = { ...defaultDeps, ...overrides }

  const loading = ref(false)
  const refreshing = ref(false)
  const loadError = ref<'unavailable' | 'failed' | null>(null)
  const data = shallowRef<PoliciesResponse | null>(null)
  const drafts = ref<Record<string, PolicySpec>>({})
  /** Entry each draft was started from; older than the latest entry when someone else wrote meanwhile. */
  const bases = shallowRef<Record<string, AvailablePolicy>>({})
  const groups = ref<AdminGroup[]>([])
  const groupsFailed = ref(false)
  const preview = ref<PreviewState | null>(null)
  const receipts = ref<Record<string, WriteReceipt>>({})
  const histories = ref<Record<string, HistoryResponse | 'loading' | 'failed'>>({})
  /** Re-reads already made for each deadline tag. */
  const rereads = new Map<string, number>()

  const killSwitch = computed(() => data.value?.static.kill_switch === true)
  const refreshSeconds = computed(() => data.value?.refresh_interval_s ?? DEFAULT_REFRESH_SECONDS)
  const master = computed(() => policy(MASTER_KEY))
  /** The stored master setting (what the switch shows), not whether it is in effect. */
  const masterOn = computed(() => master.value?.stored.spec.enabled === true)
  /** A group referenced by any group's availability_fallback_group_id is a fallback group (§7.1, C-13). */
  const fallbackGroupIds = computed(() => {
    const ids = new Set<number>()
    for (const g of groups.value) if (typeof g.availability_fallback_group_id === 'number') ids.add(g.availability_fallback_group_id)
    return ids
  })

  function entry(key: string): PolicyEntry | undefined {
    return data.value?.policies.find((p) => p.key === key)
  }

  /** The entry when this build ships the key; keys it lacks have no stored value, defaults or schema. */
  function policy(key: string): AvailablePolicy | undefined {
    const e = entry(key)
    return isAvailable(e) ? e : undefined
  }

  function baseOf(key: string): AvailablePolicy | undefined {
    return bases.value[key] ?? policy(key)
  }

  function draftOf(key: string): PolicySpec {
    return drafts.value[key] ?? policy(key)?.stored.spec ?? {}
  }

  /** The admin's own edits, relative to the value they started from. */
  function changes(key: string) {
    const base = baseOf(key)
    return base ? changedFields(base, draftOf(key)) : []
  }

  function issues(key: string) {
    const e = policy(key)
    return e ? inlineIssues(e, draftOf(key), baseOf(key)?.stored.spec) : []
  }

  /** Someone else wrote this key while the admin had a draft open; the preview will merge. */
  function staleBase(key: string): AvailablePolicy | null {
    const base = bases.value[key]
    const latest = policy(key)
    if (!base || !latest || base.stored.rev === latest.stored.rev || !changes(key).length) return null
    return latest
  }

  /** Effective state shown for a key; the static kill switch wins even if the server lags (C-11). */
  function stateOf(key: string): string {
    const e = policy(key)
    if (!e) return 'not_in_build'
    if (killSwitch.value) return 'killed'
    return e.effective.state
  }

  const dirtyKeys = computed(() => (data.value?.policies ?? []).filter((p) => p.key !== MASTER_KEY && changes(p.key).length).map((p) => p.key))

  /**
   * Takes a fresh list: server fields always update; a draft survives unless
   * its key is in `reset` or it has no edits (M-2). The key under preview
   * keeps its draft as well.
   */
  function applyResponse(response: PoliciesResponse, reset: 'all' | string[] = []) {
    const nextDrafts: Record<string, PolicySpec> = {}
    const nextBases: Record<string, AvailablePolicy> = {}
    const current = new Map((data.value?.policies ?? []).map((p) => [p.key, p]))
    // A slow read that started before a write must not roll a key back to an older rev.
    const policies = response.policies.map((p) => {
      const known = current.get(p.key)
      return known?.stored && p.stored && known.stored.rev > p.stored.rev ? known : p
    })
    for (const p of policies) {
      if (!isAvailable(p)) continue
      const keep =
        reset !== 'all' &&
        !reset.includes(p.key) &&
        drafts.value[p.key] !== undefined &&
        (preview.value?.key === p.key || (p.key !== MASTER_KEY && changes(p.key).length > 0))
      nextDrafts[p.key] = keep ? drafts.value[p.key] : cloneValue(p.stored.spec)
      nextBases[p.key] = keep ? bases.value[p.key] ?? p : p
    }
    data.value = { ...response, policies }
    drafts.value = nextDrafts
    bases.value = nextBases
  }

  async function loadGroupsInto() {
    try {
      groups.value = await deps.loadGroups()
      groupsFailed.value = false
    } catch {
      groupsFailed.value = true
    }
  }

  /** First load and the explicit reload: every draft is replaced by the stored value. */
  async function load() {
    if (loading.value) return
    loading.value = true
    const [policies] = await Promise.allSettled([deps.api.listPolicies(), loadGroupsInto()])
    if (policies.status === 'fulfilled') {
      applyResponse(policies.value, 'all')
      loadError.value = null
    } else {
      loadError.value = isNotFound(policies.reason) ? 'unavailable' : 'failed'
    }
    loading.value = false
  }

  let refreshAgain = false

  /** Re-reads every key in the background and keeps the drafts that have edits (M-2). */
  async function refresh(): Promise<void> {
    if (loading.value) return
    if (refreshing.value) {
      // A read already in flight may predate the change that asked for this one.
      refreshAgain = true
      return
    }
    refreshing.value = true
    try {
      do {
        refreshAgain = false
        try {
          applyResponse(await deps.api.listPolicies())
        } catch {
          // The panel keeps the last good state; the next write, countdown or reload tries again.
        }
      } while (refreshAgain)
    } finally {
      refreshing.value = false
    }
  }

  function replaceEntry(next: PolicyEntry) {
    if (!data.value) return
    data.value = { ...data.value, policies: data.value.policies.map((p) => (p.key === next.key ? next : p)) }
  }

  function resetKey(e: AvailablePolicy) {
    drafts.value = { ...drafts.value, [e.key]: cloneValue(e.stored.spec) }
    bases.value = { ...bases.value, [e.key]: e }
  }

  function commitDraft(key: string, draft: PolicySpec, base: AvailablePolicy) {
    drafts.value = { ...drafts.value, [key]: draft }
    bases.value = { ...bases.value, [key]: base }
  }

  function update(key: string, path: string, value: unknown) {
    if (preview.value?.submitting || !policy(key)) return
    // Under the static kill switch the write is still saved and applies once it is lifted (FE-Q4).
    drafts.value = { ...drafts.value, [key]: setPath(draftOf(key), path, value) }
  }

  function discard(key: string) {
    const e = policy(key)
    if (e) resetKey(e)
  }

  async function runValidate(state: PreviewState) {
    state.validating = true
    state.validateFailure = null
    state.result = null
    try {
      state.result = await deps.api.validatePolicy(state.key, { expected_rev: state.entry.stored.rev, spec: state.spec })
    } catch (error) {
      state.validateFailure = toWriteFailure(error)
    } finally {
      state.validating = false
    }
  }

  function conflictOf(base: AvailablePolicy, latest: AvailablePolicy, merge: ReturnType<typeof mergeDraft>): PolicyConflict {
    const { updated_at, updated_by, reason } = latest.stored
    return {
      baseRev: base.stored.rev,
      latestRev: latest.stored.rev,
      latest: { updated_at, updated_by, reason },
      writes: null,
      theirFields: merge.theirFields,
      merged: merge.merged,
      overlaps: merge.overlaps,
    }
  }

  /** Builds the preview for the admin's draft against the latest entry, merging three ways if the base moved. */
  function buildPreview(key: string): PreviewState | null {
    const latest = policy(key)
    const base = baseOf(key)
    if (!latest || !base) return null
    let draft = draftOf(key)
    let conflict: PolicyConflict | null = null
    let pendingDraft: PolicySpec | null = null
    if (base.stored.rev !== latest.stored.rev) {
      const merge = mergeDraft(base, draft, latest)
      conflict = conflictOf(base, latest, merge)
      if (merge.overlaps.length) pendingDraft = merge.draft
      else {
        draft = merge.draft
        commitDraft(key, draft, latest)
      }
    }
    const pending = changedFields(latest, draft)
    if (!pendingDraft && !pending.length && !conflict) return null
    return {
      key,
      entry: latest,
      spec: pendingDraft ? {} : partialSpec(draft, pending),
      validating: false,
      result: null,
      validateFailure: null,
      submitting: false,
      failure: null,
      conflict,
      pendingDraft,
      nothingLeft: !pendingDraft && !pending.length,
    }
  }

  /** The writes the admin skipped over, for the conflict summary (history `after_rev`, §7.6). */
  async function loadIntervening(state: PreviewState) {
    const conflict = state.conflict
    if (!conflict) return
    try {
      const history = await deps.api.listHistory(state.key, 20, conflict.baseRev)
      conflict.writes = history.items.filter((i) => i.status_code < 400 && typeof i.rev_after === 'number' && i.rev_after > conflict.baseRev)
    } catch {
      // The latest stored envelope (actor, time, reason) still describes the newest write.
      conflict.writes = null
    }
  }

  async function startPreview(key: string, staleRetries = 0) {
    preview.value = buildPreview(key)
    const state = preview.value
    if (!state) return
    if (state.conflict) void loadIntervening(state)
    if (!state.pendingDraft && !state.nothingLeft) await validateOrMerge(state, staleRetries)
  }

  /** Validates; `rev_stale` means the stored value moved, so merge again before any confirmation (§11.7). */
  async function validateOrMerge(state: PreviewState, staleRetries: number) {
    await runValidate(state)
    const result = state.result
    if (!result?.rev_stale || preview.value !== state) return
    if (staleRetries < MAX_STALE_RETRIES) {
      await reloadAndMerge(state, staleRetries + 1)
    } else {
      state.result = null
      state.validateFailure = { kind: 'conflict', currentRev: result.current_rev }
    }
  }

  async function reloadAndMerge(state: PreviewState, staleRetries = 0) {
    state.failure = null
    state.validateFailure = null
    // The old validation is stale now: no submit until the merge has been validated again.
    state.result = null
    state.validating = true
    let latest: PolicyEntry
    try {
      latest = await deps.api.getPolicy(state.key)
    } catch {
      // Nothing to merge against yet; keep the conflict visible with its refresh button.
      state.validating = false
      state.validateFailure = { kind: 'conflict', currentRev: null }
      return
    }
    replaceEntry(latest)
    // The admin may have closed this dialog, or opened another one, while the read was in flight.
    if (preview.value !== state) return
    await startPreview(state.key, staleRetries)
  }

  /** Opens the confirmation step: computes the change set and asks the server to validate it (no write, no audit). */
  async function openPreview(key: string, override?: PolicySpec) {
    const e = policy(key)
    if (!e || preview.value) return
    if (override) commitDraft(key, override, e)
    await startPreview(key)
  }

  function setChoice(field: string, choice: 'mine' | 'theirs') {
    const overlap = preview.value?.conflict?.overlaps.find((o) => o.field === field)
    if (overlap && preview.value?.pendingDraft) overlap.choice = choice
  }

  /** Applies the per-field choices of a conflict, then validates what is left to write. */
  async function resolveConflict() {
    const state = preview.value
    if (!state?.conflict || !state.pendingDraft || state.conflict.overlaps.some((o) => !o.choice)) return
    const draft = applyChoices(state.pendingDraft, state.conflict.overlaps)
    commitDraft(state.key, draft, state.entry)
    const pending = changedFields(state.entry, draft)
    state.pendingDraft = null
    state.spec = partialSpec(draft, pending)
    state.nothingLeft = !pending.length
    if (!state.nothingLeft) await validateOrMerge(state, 0)
  }

  function closePreview() {
    const state = preview.value
    if (!state || state.submitting) return
    // The master switch has no draft of its own outside the dialog.
    if (state.key === MASTER_KEY) discard(MASTER_KEY)
    preview.value = null
  }

  async function submit(input: { reason: string; checkbox: boolean; typed: string }): Promise<boolean> {
    const state = preview.value
    if (!state || state.submitting || state.pendingDraft || state.nothingLeft || !state.result?.valid) return false
    state.submitting = true
    state.failure = null
    let conflicted = false
    const savedOnly = killSwitch.value
    try {
      const saved = await deps.api.putPolicy(state.key, {
        expected_rev: state.entry.stored.rev,
        spec: state.spec,
        reason: input.reason.trim() || undefined,
        confirm: { checkbox: input.checkbox, typed: input.typed.trim() || null },
      })
      replaceEntry(saved)
      if (isAvailable(saved)) resetKey(saved)
      const at = deps.now()
      receipts.value = {
        ...receipts.value,
        [state.key]: {
          rev: saved.stored?.rev ?? state.entry.stored.rev + 1,
          at,
          effectiveBy: new Date(at.getTime() + refreshSeconds.value * 1000),
          // No countdown under the kill switch: there is no "in effect by" to wait for.
          settled: savedOnly,
          savedOnly,
        },
      }
      if (histories.value[state.key]) void loadHistory(state.key)
      preview.value = null
      // Other keys depend on this write (master gates three of them); re-read them all.
      void refresh()
      return true
    } catch (error) {
      const failure = toWriteFailure(error)
      if (failure.kind === 'confirm' && state.result) {
        // The server recomputed the confirmation rules; show the controls it still wants.
        const c = state.result.confirm
        state.result = {
          ...state.result,
          confirm: {
            reason_required: c.reason_required || failure.missing.includes('reason'),
            checkbox_required: c.checkbox_required || failure.missing.includes('checkbox'),
            type_to_confirm: c.type_to_confirm ?? failure.typeToConfirm,
          },
        }
      }
      state.failure = failure
      conflicted = failure.kind === 'conflict'
      return false
    } finally {
      if (preview.value === state) state.submitting = false
      // A 409 goes straight to the three-way merge; the typed reason stays.
      if (conflicted && preview.value === state) void reloadAndMerge(state)
    }
  }

  /** Retries the merge after a failed reload (or a conflict that could not be merged automatically). */
  async function refreshConflict() {
    const state = preview.value
    if (!state || state.submitting) return
    await reloadAndMerge(state)
  }

  async function loadHistory(key: string) {
    histories.value = { ...histories.value, [key]: 'loading' }
    try {
      histories.value = { ...histories.value, [key]: await deps.api.listHistory(key, 20) }
    } catch {
      histories.value = { ...histories.value, [key]: 'failed' }
    }
  }

  /** When the key is expected to be in effect everywhere: a fresh receipt, or the server's pending_reload. */
  function deadlineOf(key: string): { tag: string; at: Date; receipt: boolean } | null {
    const receipt = receipts.value[key]
    if (receipt && !receipt.settled) return { tag: `receipt:${key}:${receipt.rev}`, at: receipt.effectiveBy, receipt: true }
    const e = policy(key)
    if (e?.effective.state === 'pending_reload' && e.stored.updated_at) {
      const at = new Date(new Date(e.stored.updated_at).getTime() + refreshSeconds.value * 1000)
      if (!Number.isNaN(at.getTime())) return { tag: `pending:${key}:${e.stored.rev}`, at, receipt: false }
    }
    return null
  }

  /**
   * When the next re-read for a deadline is due, or null once they are used up: a receipt re-reads once; a key still
   * pending_reload re-reads each refresh period, a bounded number of times, in case an instance loads late (n-new-2).
   */
  function nextReread(deadline: { tag: string; at: Date; receipt: boolean }): Date | null {
    const done = rereads.get(deadline.tag) ?? 0
    if (done >= (deadline.receipt ? 1 : PENDING_REREADS)) return null
    return new Date(deadline.at.getTime() + done * refreshSeconds.value * 1000)
  }

  /** Whether a countdown is running or due, so the panel's clock needs second resolution. */
  function hasCountdown(): boolean {
    return (data.value?.policies ?? []).some((p) => {
      const deadline = deadlineOf(p.key)
      return !!deadline && !!nextReread(deadline)
    })
  }

  /** Driven by the panel's clock: once a countdown runs out, re-read the server state (M-2). */
  function tick(now: Date) {
    let due = false
    for (const p of data.value?.policies ?? []) {
      const deadline = deadlineOf(p.key)
      const at = deadline && nextReread(deadline)
      if (!deadline || !at || now < at) continue
      rereads.set(deadline.tag, (rereads.get(deadline.tag) ?? 0) + 1)
      if (deadline.receipt) receipts.value = { ...receipts.value, [p.key]: { ...receipts.value[p.key], settled: true } }
      due = true
    }
    if (due) void refresh()
  }

  return {
    loading,
    refreshing,
    loadError,
    data,
    drafts,
    groups,
    groupsFailed,
    preview,
    receipts,
    histories,
    killSwitch,
    refreshSeconds,
    master,
    masterOn,
    fallbackGroupIds,
    dirtyKeys,
    entry,
    policy,
    baseOf,
    draftOf,
    changes,
    issues,
    staleBase,
    stateOf,
    load,
    refresh,
    update,
    discard,
    openPreview,
    setChoice,
    resolveConflict,
    closePreview,
    submit,
    refreshConflict,
    loadHistory,
    deadlineOf,
    hasCountdown,
    tick,
  }
}

export type PoliciesState = ReturnType<typeof usePolicies>
