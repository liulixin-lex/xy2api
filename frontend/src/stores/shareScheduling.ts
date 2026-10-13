import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type {
  ShareGroupSummary,
  SharePolicyView,
  SharePreview,
  ShareRebalanceMode,
  ShareSchedulingSettings
} from '@/api/admin/shareScheduling'
import {
  cloneShareJSON,
  draftFingerprint,
  draftFromView,
  isShareConflict,
  isShareUnavailable,
  policyFromDraft,
  refreshShareDefaults,
  sharePolicyProblems,
  validateShareDraft,
  type ShareDraft,
  type ShareDraftRow,
  type ShareProblem
} from '@/utils/shareScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

/** One group's loaded policy and the administrator's draft of it. */
export interface ShareGroupEntry {
  view: SharePolicyView
  draft: ShareDraft
  /** Fingerprint of the loaded policy; the draft is dirty when it differs. */
  baseline: string
  /** Version the next save expects (CAS). */
  version: number
  /** Set after a 409: the latest version, or 0 when it could not be read. */
  conflict: number | null
}

export type ShareLoadState = 'idle' | 'loading' | 'failed' | 'unavailable'

type ShareRowRule = Pick<ShareDraftRow, 'configured' | 'tier' | 'weight' | 'drain' | 'drain_until' | 'cache_domain'>

const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
const copy = (value: unknown): unknown => (value === undefined ? undefined : cloneShareJSON(value))
const isRecord = (value: unknown) => value !== null && typeof value === 'object' && !Array.isArray(value)

function rowRule(row: ShareDraftRow): ShareRowRule {
  return { configured: row.configured, tier: row.tier, weight: row.weight, drain: row.drain, drain_until: row.drain_until, cache_domain: row.cache_domain }
}

/** Three-way merge: unchanged fields take theirs; a field both changed keeps mine. */
function mergeField(before: unknown, mine: unknown, theirs: unknown): unknown {
  if (equal(mine, before)) return copy(theirs)
  if ([before, mine, theirs].every(isRecord)) {
    const b = before as Record<string, unknown>, m = mine as Record<string, unknown>, t = theirs as Record<string, unknown>
    return Object.fromEntries([...new Set([...Object.keys(b), ...Object.keys(m), ...Object.keys(t)])]
      .map(key => [key, mergeField(b[key], m[key], t[key])]))
  }
  return copy(mine)
}

/**
 * Reapplies the administrator's own edits (draft versus the version it was
 * based on) onto a freshly loaded policy, field by field. Fields the draft
 * did not change take the latest values, so another administrator's edits
 * survive; a field both changed keeps the draft's value, and the preview
 * shows the result before it is saved.
 */
export function rebaseShareDraft(latest: SharePolicyView, edited: ShareDraft, base: ShareDraft): ShareDraft {
  const draft = draftFromView(latest)
  const { accounts: _base, ...basePolicy } = base.policy
  const { accounts: _mine, ...minePolicy } = edited.policy
  const { accounts: _latest, ...latestPolicy } = draft.policy
  Object.assign(draft.policy, mergeField(basePolicy, minePolicy, latestPolicy))
  const baseRows = new Map(base.rows.map(row => [row.account_id, row]))
  const editedRows = new Map(edited.rows.map(row => [row.account_id, row]))
  for (const row of draft.rows) {
    const own = editedRows.get(row.account_id), before = baseRows.get(row.account_id)
    if (!own || !before || equal(rowRule(own), rowRule(before))) continue
    // An edited row keeps its explicit or default choice, so a concurrent
    // reset cannot silently drop the edit.
    Object.assign(row, mergeField(rowRule(before), rowRule(own), rowRule(row)), { configured: own.configured })
  }
  refreshShareDefaults(draft)
  return draft
}

export const useShareSchedulingStore = defineStore('shareScheduling', () => {
  const settings = ref<ShareSchedulingSettings | null>(null)
  const settingsFailed = ref(false)
  const groups = ref<ShareGroupSummary[]>([])
  const groupsState = ref<ShareLoadState>('idle')
  const selected = ref<number | null>(null)
  const entries = ref<Record<number, ShareGroupEntry>>({})
  const policyState = ref<ShareLoadState>('idle')
  const policyError = ref('')
  const previewing = ref(false)
  const saving = ref(false)
  const preview = ref<SharePreview | null>(null)
  const previewFingerprint = ref('')
  const problems = ref<ShareProblem[]>([])
  const serverProblems = ref<string[]>([])
  const actionError = ref('')
  const savedVersion = ref<number | null>(null)
  const rebasedVersion = ref<number | null>(null)
  let policyRequest = 0
  let actionEpoch = 0
  let previewContext: { groupId: number; version: number; fingerprint: string; epoch: number } | null = null
  function invalidateActions() {
    actionEpoch++
    preview.value = null
    previewContext = null
    previewFingerprint.value = ''
    previewing.value = false
    saving.value = false
  }
  function owns(epoch: number, groupId: number, entry: ShareGroupEntry) {
    return epoch === actionEpoch && selected.value === groupId && current.value === entry
  }

  const current = computed(() => (selected.value === null ? null : entries.value[selected.value] ?? null))
  const isDirty = (entry: ShareGroupEntry | null | undefined) => Boolean(entry && draftFingerprint(entry.draft) !== entry.baseline)
  const dirty = computed(() => isDirty(current.value))
  const draftGroups = computed(() => new Set(Object.entries(entries.value).filter(([, entry]) => isDirty(entry)).map(([id]) => Number(id))))
  const anyDirty = computed(() => draftGroups.value.size > 0)

  function clearFeedback() {
    problems.value = []
    serverProblems.value = []
    actionError.value = ''
    savedVersion.value = null
    rebasedVersion.value = null
  }

  function accept(view: SharePolicyView): ShareGroupEntry {
    const draft = draftFromView(view)
    const entry: ShareGroupEntry = { view, draft, baseline: draftFingerprint(draft), version: view.version, conflict: null }
    entries.value[view.group_id] = entry
    const summary = groups.value.find(group => group.group_id === view.group_id)
    if (summary) {
      Object.assign(summary, { scheduler: view.scheduler, version: view.version, configured: view.version > 0, effective: view.effective, invalid: Boolean(view.invalid) })
    }
    return entry
  }

  async function loadSettings() {
    settingsFailed.value = false
    try {
      settings.value = await shareSchedulingAPI.getSettings()
    } catch {
      settingsFailed.value = true
    }
  }

  async function loadGroups() {
    groupsState.value = 'loading'
    try {
      groups.value = await shareSchedulingAPI.listGroups()
      groupsState.value = 'idle'
    } catch (err) {
      groupsState.value = isShareUnavailable(err) ? 'unavailable' : 'failed'
    }
  }

  async function loadPolicy(groupId: number) {
    const request = ++policyRequest
    policyState.value = 'loading'
    policyError.value = ''
    try {
      const view = await shareSchedulingAPI.getPolicy(groupId)
      if (request !== policyRequest || selected.value !== groupId) return
      accept(view)
      policyState.value = 'idle'
    } catch (err) {
      if (request !== policyRequest || selected.value !== groupId) return
      policyState.value = isShareUnavailable(err) ? 'unavailable' : 'failed'
      policyError.value = extractApiErrorMessage(err, '')
    }
  }

  /** Selects a group; a kept draft is shown as is instead of reloading. */
  async function select(groupId: number) {
    policyRequest++
    invalidateActions()
    selected.value = groupId
    clearFeedback()
    if (entries.value[groupId]) {
      policyState.value = 'idle'
      return
    }
    await loadPolicy(groupId)
  }

  /** Reloads the selected group, discarding its draft. */
  async function reload() {
    if (selected.value === null) return
    invalidateActions()
    clearFeedback()
    await loadPolicy(selected.value)
  }

  function discard() {
    const entry = current.value
    if (!entry) return
    entry.draft = draftFromView(entry.view)
    entry.conflict = null
    invalidateActions()
    clearFeedback()
  }

  /** After a conflict, loads the latest version and reapplies the draft. */
  async function rebase() {
    const entry = current.value
    if (!entry || selected.value === null) return
    invalidateActions()
    const groupId = selected.value, epoch = actionEpoch
    const edited = cloneShareJSON(entry.draft), fingerprint = draftFingerprint(entry.draft)
    actionError.value = ''
    try {
      const latest = await shareSchedulingAPI.getPolicy(groupId)
      if (!owns(epoch, groupId, entry) || draftFingerprint(entry.draft) !== fingerprint) return
      const draft = rebaseShareDraft(latest, edited, draftFromView(entry.view))
      accept(latest).draft = draft
      clearFeedback()
      rebasedVersion.value = latest.version
    } catch (err) {
      if (owns(epoch, groupId, entry)) actionError.value = extractApiErrorMessage(err, '')
    }
  }

  /** Validates the draft and asks the server for its preview. */
  async function requestPreview(): Promise<boolean> {
    const entry = current.value
    if (!entry || selected.value === null || previewing.value || saving.value) return false
    clearFeedback()
    problems.value = validateShareDraft(entry.draft)
    if (problems.value.length) return false
    invalidateActions()
    previewing.value = true
    const groupId = selected.value, epoch = actionEpoch, version = entry.version
    const fingerprint = draftFingerprint(entry.draft)
    try {
      const result = await shareSchedulingAPI.preview(groupId, policyFromDraft(entry.draft))
      if (!owns(epoch, groupId, entry) || entry.version !== version || draftFingerprint(entry.draft) !== fingerprint) return false
      preview.value = result
      previewFingerprint.value = fingerprint
      previewContext = { groupId, version, fingerprint, epoch }
      return true
    } catch (err) {
      if (!owns(epoch, groupId, entry)) return false
      serverProblems.value = sharePolicyProblems(err)
      if (!serverProblems.value.length) actionError.value = extractApiErrorMessage(err, '')
      return false
    } finally {
      if (epoch === actionEpoch) previewing.value = false
    }
  }

  /** Saves the previewed draft with the expected version. */
  async function save(): Promise<boolean> {
    const entry = current.value
    if (!entry || selected.value === null || saving.value) return false
    const groupId = selected.value, epoch = actionEpoch
    const fingerprint = draftFingerprint(entry.draft)
    if (!preview.value || !previewContext || previewContext.groupId !== groupId ||
        previewContext.version !== entry.version || previewContext.epoch !== epoch || previewFingerprint.value !== fingerprint) return false
    const submitted = cloneShareJSON(entry.draft)
    saving.value = true
    actionError.value = ''
    try {
      const view = await shareSchedulingAPI.updatePolicy(groupId, entry.version, policyFromDraft(submitted))
      if (!owns(epoch, groupId, entry)) return false
      const edited = cloneShareJSON(entry.draft)
      const next = accept(view)
      // Edits made while the save was in flight stay as a draft on top of it.
      if (draftFingerprint(edited) !== fingerprint) next.draft = rebaseShareDraft(view, edited, submitted)
      invalidateActions()
      savedVersion.value = view.version
      return true
    } catch (err) {
      if (!owns(epoch, groupId, entry)) return false
      preview.value = null
      previewContext = null
      if (isShareConflict(err)) {
        entry.conflict = 0
        try {
          const latest = await shareSchedulingAPI.getPolicy(groupId)
          if (owns(epoch, groupId, entry)) entry.conflict = latest.version
        } catch { /* Draft and unknown conflict version are retained. */ }
      } else {
        serverProblems.value = sharePolicyProblems(err)
        if (!serverProblems.value.length) actionError.value = extractApiErrorMessage(err, '')
      }
      return false
    } finally {
      if (epoch === actionEpoch) saving.value = false
    }
  }

  function closePreview() {
    invalidateActions()
  }

  /** Forgets every loaded policy and draft (the page was left). */
  function clear() {
    policyRequest++
    invalidateActions()
    selected.value = null
    entries.value = {}
    policyState.value = 'idle'
    preview.value = null
    clearFeedback()
  }

  async function rebalance(mode: ShareRebalanceMode): Promise<number> {
    if (selected.value === null) throw new Error('no group selected')
    const result = await shareSchedulingAPI.rebalance(selected.value, mode)
    return result.cleared
  }

  return {
    settings,
    settingsFailed,
    groups,
    groupsState,
    selected,
    entries,
    policyState,
    policyError,
    previewing,
    saving,
    preview,
    problems,
    serverProblems,
    actionError,
    savedVersion,
    rebasedVersion,
    current,
    dirty,
    draftGroups,
    anyDirty,
    loadSettings,
    loadGroups,
    select,
    reload,
    discard,
    rebase,
    requestPreview,
    save,
    closePreview,
    clear,
    rebalance
  }
})
