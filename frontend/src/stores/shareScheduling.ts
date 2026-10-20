import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import shareSchedulingAPI from '@/api/admin/shareScheduling'
import type {
  ShareGroupSummary,
  ShareGroupUpdate,
  SharePolicyView,
  SharePreview,
  ShareReport,
  ShareScheduler,
  ShareSchedulingSettings
} from '@/api/admin/shareScheduling'
import { isShareConflict, isShareUnavailable } from '@/utils/shareScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

export type ShareLoadState = 'idle' | 'loading' | 'failed' | 'unavailable'

/** A pending change of the selected group, waiting for the administrator to confirm it. */
export interface SharePendingChange {
  groupId: number
  update: ShareGroupUpdate
  preview: SharePreview | null
  previewError: string
  /** The main switch is off and turning the group to share turns it on too. */
  enableGlobal: boolean
}

/**
 * Share scheduling page state. A group has two settings, the scheduler and
 * session keeping; each change is confirmed and saved at once, so there is
 * no draft. Saves carry the loaded version and stop on a conflict.
 */
export const useShareSchedulingStore = defineStore('shareScheduling', () => {
  const settings = ref<ShareSchedulingSettings | null>(null)
  const settingsFailed = ref(false)
  const groups = ref<ShareGroupSummary[]>([])
  const groupsState = ref<ShareLoadState>('idle')
  const selected = ref<number | null>(null)
  const view = ref<SharePolicyView | null>(null)
  const policyState = ref<ShareLoadState>('idle')
  const policyError = ref('')
  const pending = ref<SharePendingChange | null>(null)
  const previewing = ref(false)
  const saving = ref(false)
  const actionError = ref('')
  const conflict = ref(false)
  const report = ref<ShareReport | null>(null)
  const reportWindow = ref('1h')
  const reportState = ref<ShareLoadState>('idle')
  const reportError = ref('')
  let policyRequest = 0
  let reportRequest = 0
  let actionEpoch = 0

  const groupNames = computed(() => new Map(groups.value.map(g => [g.group_id, g.name])))
  const activeGroups = computed(() => groups.value.filter(g => g.scheduler === 'share'))
  const idleGroups = computed(() => groups.value.filter(g => g.scheduler !== 'share'))

  function resetAction() {
    actionEpoch++
    pending.value = null
    previewing.value = false
    saving.value = false
    actionError.value = ''
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

  function syncSummary(next: SharePolicyView) {
    const summary = groups.value.find(group => group.group_id === next.group_id)
    if (summary) Object.assign(summary, { scheduler: next.scheduler, session_keep: next.session_keep, version: next.version, configured: next.version > 0, effective: next.effective })
  }

  async function loadPolicy(groupId: number) {
    const request = ++policyRequest
    policyState.value = 'loading'
    policyError.value = ''
    try {
      const next = await shareSchedulingAPI.getPolicy(groupId)
      if (request !== policyRequest || selected.value !== groupId) return
      view.value = next
      conflict.value = false
      syncSummary(next)
      policyState.value = 'idle'
    } catch (err) {
      if (request !== policyRequest || selected.value !== groupId) return
      policyState.value = isShareUnavailable(err) ? 'unavailable' : 'failed'
      policyError.value = extractApiErrorMessage(err, '')
    }
  }

  async function loadReport() {
    const groupId = selected.value
    if (groupId === null) return
    const request = ++reportRequest
    reportState.value = 'loading'
    reportError.value = ''
    try {
      const next = await shareSchedulingAPI.report(groupId, reportWindow.value)
      if (request !== reportRequest || selected.value !== groupId) return
      report.value = next
      reportState.value = 'idle'
    } catch (err) {
      if (request !== reportRequest || selected.value !== groupId) return
      reportState.value = isShareUnavailable(err) ? 'unavailable' : 'failed'
      reportError.value = extractApiErrorMessage(err, '')
    }
  }

  async function select(groupId: number) {
    if (selected.value === groupId && view.value?.group_id === groupId) return
    resetAction()
    selected.value = groupId
    view.value = null
    report.value = null
    conflict.value = false
    await Promise.all([loadPolicy(groupId), loadReport()])
  }

  /** Reloads the selected group after a conflict or an account change. */
  async function reload() {
    if (selected.value === null) return
    resetAction()
    await Promise.all([loadPolicy(selected.value), loadReport()])
  }

  async function setReportWindow(window: string) {
    reportWindow.value = window
    await loadReport()
  }

  /**
   * Asks the server what a change would do and holds it for confirmation.
   * Switching to share while the main switch is off turns the switch on in
   * the same confirmation.
   */
  async function requestChange(change: { scheduler?: ShareScheduler; session_keep?: boolean }) {
    const current = view.value
    if (!current || selected.value === null || saving.value) return
    resetAction()
    const epoch = actionEpoch
    const groupId = selected.value
    const update: ShareGroupUpdate = {
      scheduler: change.scheduler ?? current.scheduler,
      session_keep: change.session_keep ?? current.session_keep,
      expected_version: current.version
    }
    const globalOn = settings.value?.enabled ?? current.enabled
    pending.value = { groupId, update, preview: null, previewError: '', enableGlobal: update.scheduler === 'share' && !globalOn }
    if (update.scheduler !== 'share') return
    previewing.value = true
    try {
      const preview = await shareSchedulingAPI.preview(groupId, update)
      if (epoch === actionEpoch && pending.value) {
        pending.value.preview = preview
        pending.value.enableGlobal = !preview.enabled
      }
    } catch (err) {
      if (epoch === actionEpoch && pending.value) pending.value.previewError = extractApiErrorMessage(err, '')
    } finally {
      if (epoch === actionEpoch) previewing.value = false
    }
  }

  function cancelChange() {
    resetAction()
  }

  /** Saves the confirmed change. Returns false on a conflict or an error. */
  async function confirmChange(): Promise<boolean> {
    const change = pending.value
    if (!change || saving.value || selected.value !== change.groupId) return false
    const epoch = actionEpoch
    saving.value = true
    actionError.value = ''
    try {
      if (change.enableGlobal) {
        const saved = await shareSchedulingAPI.updateSettings({ enabled: true })
        setGlobalEnabled(saved?.enabled ?? true)
      }
      const next = await shareSchedulingAPI.updatePolicy(change.groupId, change.update)
      if (epoch !== actionEpoch || selected.value !== change.groupId) return false
      view.value = next
      syncSummary(next)
      pending.value = null
      void loadReport()
      return true
    } catch (err) {
      if (epoch !== actionEpoch) return false
      if (isShareConflict(err)) {
        conflict.value = true
        pending.value = null
      } else {
        actionError.value = extractApiErrorMessage(err, '')
      }
      return false
    } finally {
      if (epoch === actionEpoch) saving.value = false
    }
  }

  function setGlobalEnabled(enabled: boolean) {
    settings.value = { ...(settings.value ?? { enabled }), enabled }
    for (const group of groups.value) group.effective = enabled && group.scheduler === 'share'
    if (view.value) view.value = { ...view.value, enabled, effective: enabled && view.value.scheduler === 'share' }
  }

  /** Forgets everything loaded for the page (the page was left). */
  function clear() {
    policyRequest++
    reportRequest++
    resetAction()
    selected.value = null
    view.value = null
    report.value = null
    policyState.value = 'idle'
    reportState.value = 'idle'
    conflict.value = false
  }

  return {
    settings,
    settingsFailed,
    groups,
    groupsState,
    selected,
    view,
    policyState,
    policyError,
    pending,
    previewing,
    saving,
    actionError,
    conflict,
    report,
    reportWindow,
    reportState,
    reportError,
    groupNames,
    activeGroups,
    idleGroups,
    loadSettings,
    loadGroups,
    select,
    reload,
    loadReport,
    setReportWindow,
    requestChange,
    cancelChange,
    confirmChange,
    setGlobalEnabled,
    clear
  }
})
