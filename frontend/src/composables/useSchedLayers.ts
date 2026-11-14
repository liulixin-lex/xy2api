import { nextTick, reactive, ref } from 'vue'

// One quality panel and one group board for the whole admin UI: opening one that is already open swaps its content
// (and brings it to the top). SchedQualityLayers renders them. Elements that open the panel carry
// data-quality-opener="<account id>:<group id or empty>": a click on one of them while the panel is open switches its
// content instead of closing it, and Escape or the close button gives focus back to it.

export interface DrawerRequest {
  accountId: number | null
  accountName: string
  /** Model tab to show; falls back to the previous tab of the same account, else the first tab. */
  model: string | null
  /** Group row to scroll to and highlight (entered from a group pill's dot). */
  groupId: number | null
  /** Bumped when an open panel is asked again (another dot): its request is applied anew. */
  raise: number
}

export interface BoardRequest {
  groupId: number | null
  groupName: string
  model: string | null
  raise: number
}

const drawer = reactive<DrawerRequest>({ accountId: null, accountName: '', model: null, groupId: null, raise: 0 })
const board = reactive<BoardRequest>({ groupId: null, groupName: '', model: null, raise: 0 })
/** Bumped after any action that changes an account's quality state, so lists and the open layers reload. */
const changed = ref(0)
/** Reloads the open layers started for a change; markSchedChanged() waits for them. */
const reloads = new Set<Promise<unknown>>()

export function openSchedDrawer(account: { id: number; name?: string }, options: { model?: string | null; groupId?: number | null } = {}) {
  const reopening = drawer.accountId !== null
  drawer.accountId = account.id
  drawer.accountName = account.name ?? ''
  drawer.model = options.model ?? null
  drawer.groupId = options.groupId ?? null
  if (reopening) drawer.raise++
}

/** The data-quality-opener value of an element that opens the panel for this account (and group). */
export const qualityOpenerKey = (accountId: number, groupId?: number | null) => `${accountId}:${groupId ?? ''}`

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

/** Signals a quality change; resolves once the reloads it started in the open layers are in. */
export async function markSchedChanged(): Promise<void> {
  changed.value++
  // Watchers on `changed` run in the next flush and hand their reloads to trackSchedReload().
  await nextTick()
  await Promise.allSettled([...reloads])
}

/** An open layer reloading because of a change. */
export function trackSchedReload(load: Promise<unknown>) {
  reloads.add(load)
  void load.finally(() => reloads.delete(load))
}

export function useSchedLayers() {
  return { drawer, board, changed }
}
