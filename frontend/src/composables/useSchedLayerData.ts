/**
 * The data of the quality panel and the group board (opened through useSchedLayers), fetched by the host page's live
 * table as two page tasks on a 5 s beat (spec 5, 7f-1), and once a minute the board's group summary and the 12-hour
 * performance of the panel's account and of the board's group (the board's only in its 12-hour view, when its 5 s
 * order task rests). They follow the table: paused with it, stopped while the tab is hidden, slowed down after
 * failures, and the page's live indicator is theirs too. The panel and the board only show what lands here.
 *
 * - A task does nothing while its layer is closed.
 * - Opening a layer, switching it to another account, group or model, and any quality change (markSchedChanged) run
 *   its task at once; a recovery waits for the panel's run.
 * - An answer for an account, group or model that is no longer open is dropped; so is one whose run was aborted.
 * - Closing a layer forgets its data: the next opening starts from the skeleton. Switching the panel to another
 *   account keeps the previous account's content until the new one lands (the panel dims it meanwhile).
 *
 * The two hosts (the accounts page and the groups page) wire it the same way:
 *
 *   const sched = useSchedLayerTasks()
 *   const table = useLiveTable({ …, tasks: [...sched.tasks] })
 *   sched.bind(table, (id) => pageRowOf(id))   // the page's row of an account when it has one (its summary is used)
 */
import { onScopeDispose, shallowReactive, watch } from 'vue'
import schedQualityAPI, { backendSummary } from '@/api/admin/schedQuality'
import type { BackendAccountDetail, BackendAccountSummary, BackendGroupSummary, BackendOrder, BackendPerformance } from '@/api/admin/schedQualityTypes'
import type { LiveTable, LiveTask } from '@/composables/useLiveTable'
import { trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import type { Account } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

export const SCHED_PANEL_TASK = 'schedPanel'
export const SCHED_BOARD_TASK = 'schedBoard'
export const SCHED_BOARD_SUMMARY_TASK = 'schedBoardSummary'
export const SCHED_PANEL_PERF_TASK = 'schedPanelPerf'
export const SCHED_BOARD_PERF_TASK = 'schedBoardPerf'
const BEAT_MS = 5000
/**
 * The group summary is over all models and costly to compute (an hour of usage records): asked on opening the board
 * and on switching to another group, then once a minute; never on the board's 5 s beat nor on a model switch.
 */
export const SUMMARY_MS = 60_000

/** A page's row of an account; `quality` is its summary when the page's table carries one. */
export type SchedPanelRow = Account & { quality?: BackendAccountSummary | null }

const panel = shallowReactive({
  /** The account the detail is for (it may differ from the open one right after a switch). */
  accountId: null as number | null,
  detail: null as BackendAccountDetail | null,
  /** The summary fetched with the detail when the page's row has none. */
  summary: null as BackendAccountSummary | null,
  /** The open account whose last run failed. */
  failedId: null as number | null,
  /** Why it failed, in the backend's words (7n-4). */
  failedReason: ''
})
const board = shallowReactive({
  /** `group id:model` of the data. */
  key: null as string | null,
  data: null as BackendOrder | null,
  /** The group over all its models (POST …/groups/summary), written on its own; null when it did not load. */
  summary: null as BackendGroupSummary | null,
  /** The group whose summary request failed last (not one still on its way, nor an answer without the group). */
  summaryFailedGroup: null as number | null,
  failedKey: null as string | null
})
/**
 * The 12-hour performance of the panel's account (`id` = account id) and of the board's group (`id` = group id): kept
 * through a failed refresh (`failedId` says so); a failure does not fail the page, the view offers its own retry.
 */
const perfState = () => ({ id: null as number | null, data: null as BackendPerformance | null, failedId: null as number | null })
const panelPerf = shallowReactive(perfState())
const boardPerf = shallowReactive(perfState())
let refreshTask: ((name: string) => Promise<void>) | null = null
const forgetPanel = () => {
  Object.assign(panel, { accountId: null, detail: null, summary: null, failedId: null, failedReason: '' })
  Object.assign(panelPerf, perfState())
}
const forgetBoard = () => {
  Object.assign(board, { key: null, data: null, summary: null, summaryFailedGroup: null, failedKey: null })
  Object.assign(boardPerf, perfState())
}

const { drawer, board: boardRequest, changed } = useSchedLayers()
const boardKey = () => (boardRequest.groupId === null ? null : `${boardRequest.groupId}:${boardRequest.model ?? ''}`)
const boardPerfOn = () => boardRequest.groupId !== null && boardRequest.view === 'perf'

/** Fetches one 12-hour performance into `state` while `id()` stays the one asked for. */
async function loadPerf(state: typeof panelPerf, id: () => number | null, fetch: (id: number, signal: AbortSignal) => Promise<BackendPerformance>, signal: AbortSignal) {
  const asked = id()
  if (asked === null) return
  try {
    const data = await fetch(asked, signal)
    if (signal.aborted || id() !== asked) return
    Object.assign(state, { id: asked, data, failedId: null })
  } catch {
    if (signal.aborted || id() !== asked) return
    state.failedId = asked
  }
}

/** The tasks for the host's live table, and bind() to hand it the table once it exists. */
export function useSchedLayerTasks() {
  let rowOf: ((id: number) => SchedPanelRow | undefined) | undefined

  const tasks: LiveTask[] = [
    {
      name: SCHED_PANEL_TASK,
      intervalMs: BEAT_MS,
      run: async (signal) => {
        const id = drawer.accountId
        if (id === null) return
        const ownSummary = rowOf?.(id)?.quality === undefined
        try {
          const [detail, summary] = await Promise.all([
            schedQualityAPI.accountDetail(id, { signal }),
            ownSummary ? backendSummary([id], { signal }) : Promise.resolve(null)
          ])
          if (signal.aborted || drawer.accountId !== id) return
          Object.assign(panel, { accountId: id, detail, summary: summary?.accounts.find((item) => item.account_id === id) ?? null, failedId: null, failedReason: '' })
        } catch (error) {
          if (signal.aborted || drawer.accountId !== id) return
          Object.assign(panel, { failedId: id, failedReason: extractApiErrorMessage(error, '') })
          throw error
        }
      }
    },
    {
      name: SCHED_BOARD_TASK,
      intervalMs: BEAT_MS,
      run: async (signal) => {
        const key = boardKey()
        // The 12-hour view has its own task: the order rests meanwhile and runs again on switching back.
        if (key === null || boardRequest.view !== 'order') return
        try {
          const data = await schedQualityAPI.groupBoard(boardRequest.groupId!, boardRequest.model ?? undefined, { signal })
          if (signal.aborted || boardKey() !== key) return
          Object.assign(board, { key, data, failedKey: null })
        } catch (error) {
          if (signal.aborted || boardKey() !== key) return
          board.failedKey = key
          throw error
        }
      }
    },
    {
      name: SCHED_BOARD_SUMMARY_TASK,
      intervalMs: SUMMARY_MS,
      // Never fails the page: without a summary the board's line reads "—".
      run: async (signal) => {
        const groupId = boardRequest.groupId
        if (groupId === null) return
        let summary: BackendGroupSummary | null = null
        let failed = false
        try {
          const value = await schedQualityAPI.groupsSummary([groupId], { signal })
          summary = value?.groups?.find((group) => group.group_id === groupId) ?? null
        } catch {
          failed = true
        }
        if (signal.aborted || boardRequest.groupId !== groupId) return
        board.summary = summary
        board.summaryFailedGroup = failed ? groupId : null
      }
    },
    {
      name: SCHED_PANEL_PERF_TASK,
      intervalMs: SUMMARY_MS,
      run: (signal) => loadPerf(panelPerf, () => drawer.accountId, (id, s) => schedQualityAPI.accountPerformance(id, { signal: s }), signal)
    },
    {
      name: SCHED_BOARD_PERF_TASK,
      intervalMs: SUMMARY_MS,
      run: (signal) => loadPerf(boardPerf, () => (boardPerfOn() ? boardRequest.groupId : null), (id, s) => schedQualityAPI.groupPerformance(id, { signal: s }), signal)
    }
  ]

  function bind(table: Pick<LiveTable<object>, 'refresh'>, row?: (id: number) => SchedPanelRow | undefined) {
    rowOf = row
    const refresh = (name: string) => table.refresh(name)
    refreshTask = refresh
    watch(() => [drawer.accountId, drawer.raise] as const, ([id]) => {
      if (id === null) forgetPanel()
      else {
        void refresh(SCHED_PANEL_TASK)
        void refresh(SCHED_PANEL_PERF_TASK)
      }
    })
    watch(() => [boardKey(), boardRequest.raise, boardRequest.view] as const, ([key]) => {
      if (key === null) forgetBoard()
      else void refresh(SCHED_BOARD_TASK)
    })
    // The 12-hour view covers every model: another model is only another filter, so it asks again only on opening it,
    // another group or another opening.
    watch(() => [boardPerfOn() ? boardRequest.groupId : null, boardRequest.raise] as const, ([groupId]) => {
      if (groupId !== null) void refresh(SCHED_BOARD_PERF_TASK)
    })
    // The summary is the group's over all models: a model switch keeps it, another group asks again.
    watch(() => boardRequest.groupId, (groupId) => {
      board.summary = null
      board.summaryFailedGroup = null
      if (groupId !== null) void refresh(SCHED_BOARD_SUMMARY_TASK)
    })
    // Any quality action (in the panel, the row menu, a batch) reloads the open layers; a recovery waits for the panel.
    watch(changed, () => {
      if (drawer.accountId !== null) {
        trackSchedReload(refresh(SCHED_PANEL_TASK))
        void refresh(SCHED_PANEL_PERF_TASK)
      }
      if (boardRequest.groupId !== null) void refresh(boardPerfOn() ? SCHED_BOARD_PERF_TASK : SCHED_BOARD_TASK)
    })
    // Leaving the page: its table is gone (the layers close after this), so is what it fetched.
    onScopeDispose(() => {
      if (refreshTask !== refresh) return
      refreshTask = null
      forgetPanel()
      forgetBoard()
    })
  }

  return { tasks, bind }
}

type RetryTask = typeof SCHED_PANEL_TASK | typeof SCHED_BOARD_TASK | typeof SCHED_PANEL_PERF_TASK | typeof SCHED_BOARD_PERF_TASK

/** What the panel and the board show, and their retry. */
export function useSchedLayerData() {
  return {
    panel,
    board,
    boardKey,
    panelPerf,
    boardPerf,
    retry: (name: RetryTask) => refreshTask?.(name) ?? Promise.resolve()
  }
}
