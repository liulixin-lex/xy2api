import { reactive, ref } from 'vue'

// One account drawer, one group board and one reset flow for the whole admin UI (design spec 0.2): opening one that
// is already open swaps its content and brings it to the top. SchedQualityLayers renders them.

export interface DrawerRequest {
  accountId: number | null
  accountName: string
  /** Model tab to show; falls back to the first tab when the detail has no such model. */
  model: string | null
  /** Group row to scroll to and highlight (entered from a capsule). */
  groupId: number | null
  /** Scroll to the groups section (entered from +N, normal or no data). */
  toGroups: boolean
  raise: number
}

export interface BoardRequest {
  groupId: number | null
  groupName: string
  model: string | null
  raise: number
}

export interface ResetRequest {
  accountId: number | null
  accountName: string
}

const drawer = reactive<DrawerRequest>({ accountId: null, accountName: '', model: null, groupId: null, toGroups: false, raise: 0 })
const board = reactive<BoardRequest>({ groupId: null, groupName: '', model: null, raise: 0 })
const reset = reactive<ResetRequest>({ accountId: null, accountName: '' })
/** Bumped after any action that changes an account's quality state, so lists refresh. */
const changed = ref(0)

export function openSchedDrawer(account: { id: number; name?: string }, options: { model?: string | null; groupId?: number | null; toGroups?: boolean } = {}) {
  const reopening = drawer.accountId !== null
  drawer.accountId = account.id
  drawer.accountName = account.name ?? ''
  drawer.model = options.model ?? null
  drawer.groupId = options.groupId ?? null
  drawer.toGroups = options.toGroups ?? false
  if (reopening) drawer.raise++
}

export function closeSchedDrawer() {
  drawer.accountId = null
  drawer.groupId = null
}

export function openSchedBoard(group: { id: number; name: string }, model?: string | null) {
  const reopening = board.groupId !== null
  board.groupId = group.id
  board.groupName = group.name
  board.model = model ?? null
  if (reopening) board.raise++
}

export function closeSchedBoard() {
  board.groupId = null
}

export function openSchedReset(account: { id: number; name: string }) {
  reset.accountId = account.id
  reset.accountName = account.name
}

export function closeSchedReset() {
  reset.accountId = null
}

export function markSchedChanged() {
  changed.value++
}

export function useSchedLayers() {
  return { drawer, board, reset, changed }
}
