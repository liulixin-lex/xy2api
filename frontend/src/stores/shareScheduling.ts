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

function rowRule(row: ShareDraftRow): ShareRowRule {
  return { configured: row.configured, tier: row.tier, weight: row.weight, drain: row.drain, drain_until: row.drain_until, cache_domain: row.cache_domain }
}

/**
 * Reapplies the administrator's own edits (draft versus the version it was
 * based on) onto a freshly loaded policy. Sections and rows the draft did not
 * change take the latest values, so another administrator's edits survive.
 */
function rebaseDraft(latest: SharePolicyView, edited: ShareDraft, base: ShareDraft): ShareDraft {
  const draft = draftFromView(latest)
  const policy = draft.policy as unknown as Record<string, unknown>
  const mine = edited.policy as unknown as Record<string, unknown>
  const before = base.policy as unknown as Record<string, unknown>
  for (const key of Object.keys(mine)) {
    if (key === 'accounts') continue
    if (JSON.stringify(mine[key]) !== JSON.stringify(before[key])) policy[key] = cloneShareJSON(mine[key])
  }
  const baseRows = new Map(base.rows.map(row => [row.account_id, JSON.stringify(rowRule(row))]))
  const editedRows = new Map(edited.rows.map(row => [row.account_id, row]))
  for (const row of draft.rows) {
    const own = editedRows.get(row.account_id)
    if (own && JSON.stringify(rowRule(own)) !== baseRows.get(row.account_id)) Object.assign(row, rowRule(own))
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
      if (request !== policyRequest) return
      accept(view)
      policyState.value = 'idle'
    } catch (err) {
      if (request !== policyRequest) return
      policyState.value = isShareUnavailable(err) ? 'unavailable' : 'failed'
      policyError.value = extractApiErrorMessage(err, '')
    }
  }

  /** Selects a group; a kept draft is shown as is instead of reloading. */
  async function select(groupId: number) {
    selected.value = groupId
    preview.value = null
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
    preview.value = null
    clearFeedback()
    await loadPolicy(selected.value)
  }

  function discard() {
    const entry = current.value
    if (!entry) return
    entry.draft = draftFromView(entry.view)
    entry.conflict = null
    preview.value = null
    clearFeedback()
  }

  /** After a conflict, loads the latest version and reapplies the draft. */
  async function rebase() {
    const entry = current.value
    if (!entry || selected.value === null) return
    const groupId = selected.value
    actionError.value = ''
    try {
      const latest = await shareSchedulingAPI.getPolicy(groupId)
      const draft = rebaseDraft(latest, entry.draft, draftFromView(entry.view))
      const next = accept(latest)
      next.draft = draft
      preview.value = null
      clearFeedback()
      rebasedVersion.value = latest.version
    } catch (err) {
      actionError.value = extractApiErrorMessage(err, '')
    }
  }

  /** Validates the draft and asks the server for its preview. */
  async function requestPreview(): Promise<boolean> {
    const entry = current.value
    if (!entry || selected.value === null || previewing.value || saving.value) return false
    clearFeedback()
    problems.value = validateShareDraft(entry.draft)
    if (problems.value.length) return false
    previewing.value = true
    const fingerprint = draftFingerprint(entry.draft)
    try {
      preview.value = await shareSchedulingAPI.preview(selected.value, policyFromDraft(entry.draft))
      previewFingerprint.value = fingerprint
      return true
    } catch (err) {
      serverProblems.value = sharePolicyProblems(err)
      if (!serverProblems.value.length) actionError.value = extractApiErrorMessage(err, '')
      return false
    } finally {
      previewing.value = false
    }
  }

  /** Saves the previewed draft with the expected version. */
  async function save(): Promise<boolean> {
    const entry = current.value
    if (!entry || selected.value === null || saving.value) return false
    const groupId = selected.value
    const fingerprint = draftFingerprint(entry.draft)
    if (!preview.value || previewFingerprint.value !== fingerprint) return false
    saving.value = true
    actionError.value = ''
    try {
      const view = await shareSchedulingAPI.updatePolicy(groupId, entry.version, policyFromDraft(entry.draft))
      accept(view)
      preview.value = null
      savedVersion.value = view.version
      return true
    } catch (err) {
      preview.value = null
      if (isShareConflict(err)) {
        entry.conflict = 0
        try {
          const latest = await shareSchedulingAPI.getPolicy(groupId)
          entry.conflict = latest.version
        } catch {
          // The draft stays; the version is only informational.
        }
      } else {
        serverProblems.value = sharePolicyProblems(err)
        if (!serverProblems.value.length) actionError.value = extractApiErrorMessage(err, '')
      }
      return false
    } finally {
      saving.value = false
    }
  }

  function closePreview() {
    preview.value = null
  }

  /** Forgets every loaded policy and draft (the page was left). */
  function clear() {
    policyRequest++
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
