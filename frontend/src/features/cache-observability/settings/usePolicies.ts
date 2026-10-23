import { computed, ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import { DEFAULT_REFRESH_SECONDS, MASTER_KEY } from './catalog'
import { isNotFound, policyApi, toWriteFailure, type PolicyApi } from './api'
import { changedFields, inlineIssues, partialSpec, rebaseDraft, setPath } from './schema'
import type { HistoryResponse, PoliciesResponse, PolicyEntry, PolicySpec, ValidateResult, WriteFailure } from './types'

export interface PreviewState {
  key: string
  /** Snapshot of the entry the preview was computed against (its rev is the expected_rev). */
  entry: PolicyEntry
  spec: PolicySpec
  validating: boolean
  result: ValidateResult | null
  validateFailure: WriteFailure | null
  submitting: boolean
  failure: WriteFailure | null
}

export interface WriteReceipt {
  rev: number
  at: Date
  effectiveBy: Date
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

export function usePolicies(overrides: Partial<PolicyDeps> = {}) {
  const deps = { ...defaultDeps, ...overrides }

  const loading = ref(false)
  const loadError = ref<'unavailable' | 'failed' | null>(null)
  const data = shallowRef<PoliciesResponse | null>(null)
  const drafts = ref<Record<string, PolicySpec>>({})
  const groups = ref<AdminGroup[]>([])
  const groupsFailed = ref(false)
  const preview = ref<PreviewState | null>(null)
  const receipts = ref<Record<string, WriteReceipt>>({})
  const histories = ref<Record<string, HistoryResponse | 'loading' | 'failed'>>({})

  const killSwitch = computed(() => data.value?.static.kill_switch === true)
  const refreshSeconds = computed(() => data.value?.refresh_interval_s ?? DEFAULT_REFRESH_SECONDS)
  const master = computed(() => entry(MASTER_KEY))
  const masterOn = computed(() => master.value?.stored.spec.enabled === true)
  const fallbackGroupIds = computed(() => {
    const ids = new Set<number>()
    for (const g of groups.value) if (typeof g.availability_fallback_group_id === 'number') ids.add(g.availability_fallback_group_id)
    return ids
  })

  function entry(key: string): PolicyEntry | undefined {
    return data.value?.policies.find((p) => p.key === key)
  }

  function draftOf(key: string): PolicySpec {
    return drafts.value[key] ?? entry(key)?.stored.spec ?? {}
  }

  function changes(key: string) {
    const e = entry(key)
    return e ? changedFields(e, draftOf(key)) : []
  }

  function issues(key: string) {
    const e = entry(key)
    return e && e.available ? inlineIssues(e, draftOf(key)) : []
  }

  const dirtyKeys = computed(() => (data.value?.policies ?? []).filter((p) => p.key !== MASTER_KEY && changes(p.key).length).map((p) => p.key))

  function resetDrafts(response: PoliciesResponse) {
    const next: Record<string, PolicySpec> = {}
    for (const p of response.policies) next[p.key] = structuredClone(p.stored.spec)
    drafts.value = next
  }

  async function load() {
    if (loading.value) return
    loading.value = true
    const [policies, directory] = await Promise.allSettled([deps.api.listPolicies(), deps.loadGroups()])
    if (directory.status === 'fulfilled') {
      groups.value = directory.value
      groupsFailed.value = false
    } else groupsFailed.value = true
    if (policies.status === 'fulfilled') {
      data.value = policies.value
      resetDrafts(policies.value)
      loadError.value = null
    } else {
      loadError.value = isNotFound(policies.reason) ? 'unavailable' : 'failed'
    }
    loading.value = false
  }

  function replaceEntry(next: PolicyEntry, keepDraft?: PolicySpec) {
    if (!data.value) return
    data.value = { ...data.value, policies: data.value.policies.map((p) => (p.key === next.key ? next : p)) }
    drafts.value = { ...drafts.value, [next.key]: keepDraft ?? structuredClone(next.stored.spec) }
  }

  function update(key: string, path: string, value: unknown) {
    if (killSwitch.value || preview.value?.submitting) return
    drafts.value = { ...drafts.value, [key]: setPath(draftOf(key), path, value) }
  }

  function discard(key: string) {
    const e = entry(key)
    if (e) drafts.value = { ...drafts.value, [key]: structuredClone(e.stored.spec) }
  }

  async function runValidate(state: PreviewState) {
    state.validating = true
    state.validateFailure = null
    try {
      state.result = await deps.api.validatePolicy(state.key, { expected_rev: state.entry.stored.rev, spec: state.spec })
    } catch (error) {
      state.result = null
      state.validateFailure = toWriteFailure(error)
    } finally {
      state.validating = false
    }
  }

  /** Opens the confirmation step: computes the change set and asks the server to validate it (no write, no audit). */
  async function openPreview(key: string, override?: PolicySpec) {
    const e = entry(key)
    if (!e || !e.available || killSwitch.value) return
    if (override) drafts.value = { ...drafts.value, [key]: override }
    const draft = draftOf(key)
    const pending = changedFields(e, draft)
    if (!pending.length) return
    preview.value = {
      key,
      entry: e,
      spec: partialSpec(draft, pending),
      validating: true,
      result: null,
      validateFailure: null,
      submitting: false,
      failure: null,
    }
    await runValidate(preview.value)
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
    if (!state || state.submitting || !state.result?.valid) return false
    state.submitting = true
    state.failure = null
    try {
      const saved = await deps.api.putPolicy(state.key, {
        expected_rev: state.entry.stored.rev,
        spec: state.spec,
        reason: input.reason.trim() || undefined,
        confirm: { checkbox: input.checkbox, typed: input.typed.trim() || null },
      })
      replaceEntry(saved)
      const at = deps.now()
      receipts.value = {
        ...receipts.value,
        [state.key]: { rev: saved.stored.rev, at, effectiveBy: new Date(at.getTime() + refreshSeconds.value * 1000) },
      }
      if (histories.value[state.key]) void loadHistory(state.key)
      preview.value = null
      return true
    } catch (error) {
      state.failure = toWriteFailure(error)
      return false
    } finally {
      if (preview.value === state) state.submitting = false
    }
  }

  /** After a 409: reload the key, re-apply the admin's edits on the new value and validate again. */
  async function refreshConflict() {
    const state = preview.value
    if (!state) return
    state.failure = null
    state.validating = true
    try {
      const fresh = await deps.api.getPolicy(state.key)
      const draft = rebaseDraft(state.entry, draftOf(state.key), fresh)
      replaceEntry(fresh, draft)
      const pending = changedFields(fresh, draft)
      if (!pending.length) {
        preview.value = null
        return
      }
      state.entry = fresh
      state.spec = partialSpec(draft, pending)
      await runValidate(state)
    } catch (error) {
      state.validating = false
      state.failure = toWriteFailure(error)
    }
  }

  async function loadHistory(key: string) {
    histories.value = { ...histories.value, [key]: 'loading' }
    try {
      histories.value = { ...histories.value, [key]: await deps.api.listHistory(key, 20) }
    } catch {
      histories.value = { ...histories.value, [key]: 'failed' }
    }
  }

  return {
    loading,
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
    draftOf,
    changes,
    issues,
    load,
    update,
    discard,
    openPreview,
    closePreview,
    submit,
    refreshConflict,
    loadHistory,
  }
}

export type PoliciesState = ReturnType<typeof usePolicies>
