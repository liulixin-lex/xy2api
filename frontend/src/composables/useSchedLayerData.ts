/**
 * The data of the quality panel and the group board (opened through useSchedLayers), fetched by the host page's live
 * table as two page tasks on a 5 s beat (spec 5, 7f-1). They follow the table: paused with it, stopped while the tab
 * is hidden, slowed down after failures, and the page's live indicator is theirs too. The panel and the board only
 * show what lands here.
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
import type { BackendAccountDetail, BackendAccountSummary, BackendOrder } from '@/api/admin/schedQualityTypes'
import type { LiveTable, LiveTask } from '@/composables/useLiveTable'
import { trackSchedReload, useSchedLayers } from '@/composables/useSchedLayers'
import type { Account } from '@/types'

export const SCHED_PANEL_TASK = 'schedPanel'
export const SCHED_BOARD_TASK = 'schedBoard'
const BEAT_MS = 5000

/** A page's row of an account; `quality` is its summary when the page's table carries one. */
export type SchedPanelRow = Account & { quality?: BackendAccountSummary | null }

const panel = shallowReactive({
  /** The account the detail is for (it may differ from the open one right after a switch). */
  accountId: null as number | null,
  detail: null as BackendAccountDetail | null,
  /** The summary fetched with the detail when the page's row has none. */
  summary: null as BackendAccountSummary | null,
  /** The open account whose last run failed. */
  failedId: null as number | null
})
const board = shallowReactive({
  /** `group id:model` of the data. */
  key: null as string | null,
  data: null as BackendOrder | null,
  failedKey: null as string | null
})
let refreshTask: ((name: string) => Promise<void>) | null = null
const forgetPanel = () => Object.assign(panel, { accountId: null, detail: null, summary: null, failedId: null })
const forgetBoard = () => Object.assign(board, { key: null, data: null, failedKey: null })

const { drawer, board: boardRequest, changed } = useSchedLayers()
const boardKey = () => (boardRequest.groupId === null ? null : `${boardRequest.groupId}:${boardRequest.model ?? ''}`)

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
          Object.assign(panel, { accountId: id, detail, summary: summary?.accounts.find((item) => item.account_id === id) ?? null, failedId: null })
        } catch (error) {
          if (signal.aborted || drawer.accountId !== id) return
          panel.failedId = id
          throw error
        }
      }
    },
    {
      name: SCHED_BOARD_TASK,
      intervalMs: BEAT_MS,
      run: async (signal) => {
        const key = boardKey()
        if (key === null) return
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
    }
  ]

  function bind(table: Pick<LiveTable<object>, 'refresh'>, row?: (id: number) => SchedPanelRow | undefined) {
    rowOf = row
    const refresh = (name: string) => table.refresh(name)
    refreshTask = refresh
    watch(() => [drawer.accountId, drawer.raise] as const, ([id]) => {
      if (id === null) forgetPanel()
      else void refresh(SCHED_PANEL_TASK)
    })
    watch(() => [boardKey(), boardRequest.raise] as const, ([key]) => {
      if (key === null) forgetBoard()
      else void refresh(SCHED_BOARD_TASK)
    })
    // Any quality action (in the panel, the row menu, a batch) reloads the open layers; a recovery waits for the panel.
    watch(changed, () => {
      if (drawer.accountId !== null) trackSchedReload(refresh(SCHED_PANEL_TASK))
      if (boardRequest.groupId !== null) void refresh(SCHED_BOARD_TASK)
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

/** What the panel and the board show, and their retry. */
export function useSchedLayerData() {
  return {
    panel,
    board,
    boardKey,
    retry: (name: typeof SCHED_PANEL_TASK | typeof SCHED_BOARD_TASK) => refreshTask?.(name) ?? Promise.resolve()
  }
}
