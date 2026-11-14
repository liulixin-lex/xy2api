/**
 * Live table engine: one store per resource for the tables that refresh in place (accounts, groups, users, usage).
 *
 * - Records are kept by id, the current page as an id list (`ids`); `rows` joins the two. Ids compare by their string
 *   form, so `12` and `'12'` are the same row. Usage records must use `source:id` as their id (`usage:12`,
 *   `unbilled:12`): the two id sequences overlap.
 * - A row object stays the same across refreshes and only fields whose value changed are assigned (arrays and objects
 *   compared by value; JSON data only). Whatever reads only unchanged fields is not re-run; a DataTable row re-renders
 *   as a whole when any field its slots read changes. No answer changes a row's id, not even its type.
 * - Rows from reload(), the membership check, sources marked `complete` and mergeRows(…, { complete: true }) are whole
 *   rows: a field the previous whole row of that record carried and this one leaves out is removed (the backend omits
 *   empty fields); a field under edit goes once the edit is over. Fields only other sources write are kept.
 * - A partial source (not `complete`) sets the fields it carries and nothing else, so it must answer every requested
 *   id with every field it is responsible for, empty values given explicitly (0, null, []): a field it leaves out is
 *   never cleared, and a count the backend omits once it drops to 0 would keep showing its last value. Fill the
 *   defaults in its fetch function (as the accounts page does with buildDefaultTodayStats).
 * - reload() is the active load (page, filter, sort, manual refresh): it replaces the id list and never clears the rows
 *   on screen (`transitioning` meanwhile); stale answers are dropped. A failed load turns `status` to 'stale' and is
 *   retried on the back-off beat; the caller of reload() also gets the error. After every load that succeeds (the
 *   engine's own retries and catch-ups too) the partial sources and the page tasks run at once, so new rows and the
 *   stats cards do not wait a whole beat.
 * - Sources refresh the rows of the current page on their own beat without changing membership or order; tasks are
 *   page-level jobs on the same scheduler (they run with no rows too). refresh(name) runs one of them now. The
 *   membership check (default every 30 s) counts rows that appeared or went (`pending`) and dims gone rows in place;
 *   applyPending() takes them in. With `tail` (usage records) the page follows the server's order while
 *   `tail.enabled()` holds; otherwise new rows are only counted (`pendingMore` once a full page is waiting: "N+").
 * - Beats are setTimeout chains started after a request ends; a source never overlaps itself. They skip while an active
 *   load runs, stop while the tab is hidden or paused, catch up at once on visible / online / resume (unless they ran
 *   within their own beat), back off 3, 6, 12, 30 s after failures, and abort on dispose. `intervalMs: 0` makes a
 *   source or task load-only: it runs after each successful active load and on refresh(name), never on a timer or a
 *   catch-up, and after a failure it still retries 3, 6, 12, 30 s later until it succeeds.
 * - edit() writes a value at once and locks the field: no background answer writes it, nor any answer to a request
 *   sent before the edit settled. The latest edit's answer is written when it succeeds; an older edit's answer never
 *   is. When the latest edit fails the field returns to the value the server last confirmed (if it still shows the
 *   optimistic value), and the error goes back to the caller.
 * - Order changes only through active loads and sortBy(compare): the comparator stays in force for every later active
 *   load until sortBy(null), and each source that runs right after a load re-sorts the page once when its values land
 *   (the comparator may read them). Other updates never re-order. A comparator that throws leaves the server's order.
 *   remove(id) takes a row off at once; answers to requests sent before it do not bring the row back.
 *
 *   const table = useLiveTable<Account>({
 *     getId: (row) => row.id,
 *     fetchPage: (signal) => listPage(signal),
 *     sources: [{ name: 'rows', intervalMs: 3000, complete: true, fetch: (ids, signal) => rowsById(ids, signal) }]
 *   })
 *   await table.reload()   // first load and every page / filter / sort change
 *   <DataTable :data="table.rows.value" :loading="table.loading.value" />
 */
import { computed, onScopeDispose, reactive, ref, shallowReactive, shallowRef, toRaw, type ComputedRef, type Ref } from 'vue'
import { useEventListener } from '@vueuse/core'

export type LiveRowId = string | number
export type LiveStatus = 'live' | 'paused' | 'stale'
/** Field values by row id: a Map, an array of [id, values] pairs, or an object keyed by id. */
export type LivePatches<T> = Iterable<readonly [LiveRowId, Partial<T>]> | Record<string, Partial<T>>

export interface LivePage<T> {
  items: T[]
  total: number
}

/**
 * Values for the rows of the current page from another endpoint. A partial source (not `complete`) must answer every
 * requested id with every field it is responsible for, empty values given explicitly (0, null, []): leaving a field
 * out never clears it. Fill the defaults in `fetch`.
 */
export interface LiveSource<T> {
  /** For refresh(name). */
  name?: string
  /**
   * Pause between the end of one request and the next one, in ms. 0 makes the source load-only: it runs after each
   * successful active load and on refresh(name), with no beat of its own (failures are still retried). A value that
   * is not a positive finite number counts as 0; values above setTimeout's limit (about 24.8 days) are capped.
   */
  intervalMs: number
  /** Every answer is a whole row: fields it leaves out are removed from the record. */
  complete?: boolean
  /** Fresh values for rows of the current page. Entries for rows not on the page are ignored. */
  fetch: (ids: LiveRowId[], signal: AbortSignal) => Promise<LivePatches<T>>
}

/**
 * A page-level job on the same beat machinery (for example the stats cards); it writes its own state. Once `signal`
 * aborts (an active load started: the filter may have changed) the job must drop its answer instead of writing it,
 * or the cards show the old filter's numbers until the next run.
 */
export interface LiveTask {
  name?: string
  /** As for sources: 0 runs the task only after each successful active load and on refresh(name). */
  intervalMs: number
  run: (signal: AbortSignal) => Promise<void>
}

export interface LiveTableOptions<T extends object> {
  getId: (row: T) => LiveRowId
  /** The current page for the current filter and sort; used by reload() and the membership check. */
  fetchPage: (signal: AbortSignal) => Promise<LivePage<T>>
  sources?: LiveSource<T>[]
  tasks?: LiveTask[]
  /** Membership check beat in ms; 0 (or anything that is not a positive finite number) turns it off. Default 30 s. */
  membershipIntervalMs?: number
  /** A row on the page that no longer matches the filter is dimmed in place. */
  matches?: (row: T) => boolean
  /** Log mode: follow the server's page while `enabled()` holds (page 1, newest first, scrolled to top). */
  tail?: { enabled: () => boolean; pageSize: () => number }
}

export interface LiveTable<T extends object> {
  rows: ComputedRef<T[]>
  ids: Readonly<Ref<readonly LiveRowId[]>>
  total: Readonly<Ref<number>>
  /** An active load is in flight. */
  loading: Readonly<Ref<boolean>>
  /** An active load is in flight while rows are shown: dim them, keep them. */
  transitioning: ComputedRef<boolean>
  status: ComputedRef<LiveStatus>
  /** Rows that appeared or went since the last active load (in log mode: new rows not shown). */
  pending: Readonly<Ref<number>>
  /** A full page of new rows is waiting in log mode; show `pending` as "N+". */
  pendingMore: Readonly<Ref<boolean>>
  record: (id: LiveRowId) => T | undefined
  isDimmed: (id: LiveRowId) => boolean
  reload: () => Promise<void>
  applyPending: () => Promise<void>
  pause: () => void
  resume: () => void
  /**
   * Runs the named sources and tasks now, paused or not, and resolves when their answer is in. Before the first load
   * or during an active load it is remembered and runs as soon as that load ends; the promise then resolves at once.
   * reload() needs no refresh() after it: partial sources, load-only sources and page tasks run once on their own
   * after every successful load.
   */
  refresh: (name: string) => Promise<void>
  /**
   * Re-orders the current page now (a local sort the user asked for) and every later active load the same way, until
   * sortBy(null) restores the server's order. Each source that runs right after a load re-sorts the page once when its
   * values land; updates never re-order. The comparator must tolerate rows whose partial fields have not arrived yet
   * (right after a page change they are missing); if it throws, the page keeps the server's order.
   */
  sortBy: (compare: ((a: T, b: T) => number) | null) => void
  /** Takes a row off the page at once (after a delete). */
  remove: (id: LiveRowId) => void
  /** Merges values from an action's answer; `complete` treats each value as a whole row. Locked fields are kept. */
  mergeRows: (patches: LivePatches<T>, options?: { complete?: boolean }) => void
  edit: <K extends keyof T & string>(id: LiveRowId, field: K, value: T[K], commit: () => Promise<Partial<T> | void>) => Promise<Partial<T> | void>
}

const BACKOFF_MS = [3000, 6000, 12000, 30000]
const DEFAULT_MEMBERSHIP_MS = 30_000
/** setTimeout's longest delay; a larger one fires at once. */
const MAX_DELAY_MS = 2 ** 31 - 1
/** A beat in ms: a positive finite number, capped at the timer limit; anything else is 0 (no beat). */
const beatMs = (ms: number | undefined) => (typeof ms === 'number' && Number.isFinite(ms) && ms > 0 ? Math.min(ms, MAX_DELAY_MS) : 0)

const pageHidden = () => typeof document !== 'undefined' && document.hidden
const isAbort = (error: unknown) => {
  const e = error as { name?: string; code?: string } | null
  return e?.name === 'AbortError' || e?.name === 'CanceledError' || e?.code === 'ERR_CANCELED'
}
const keyOf = (id: LiveRowId) => String(id)
const hasOwn = (target: object, key: string) => Object.prototype.hasOwnProperty.call(target, key)
const backoff = (failures: number) => BACKOFF_MS[Math.min(failures, BACKOFF_MS.length) - 1]

/** Value equality for JSON data (primitives, arrays, plain objects); Dates compare by time, other objects by identity. */
function sameValue(a: unknown, b: unknown): boolean {
  const x = toRaw(a)
  const y = toRaw(b)
  if (Object.is(x, y)) return true
  if (typeof x !== 'object' || typeof y !== 'object' || x === null || y === null) return false
  if (x instanceof Date || y instanceof Date) return x instanceof Date && y instanceof Date && x.getTime() === y.getTime()
  if (Array.isArray(x) || Array.isArray(y)) {
    return Array.isArray(x) && Array.isArray(y) && x.length === y.length && x.every((item, i) => sameValue(item, y[i]))
  }
  const plain = (v: object) => [Object.prototype, null].includes(Object.getPrototypeOf(v))
  if (!plain(x) || !plain(y)) return false
  const left = x as Record<string, unknown>
  const right = y as Record<string, unknown>
  const keys = Object.keys(left)
  return keys.length === Object.keys(right).length && keys.every((key) => hasOwn(right, key) && sameValue(left[key], right[key]))
}

function entriesOf<T>(patches: LivePatches<T>): Iterable<readonly [LiveRowId, Partial<T>]> {
  return Symbol.iterator in patches ? (patches as Iterable<readonly [LiveRowId, Partial<T>]>) : Object.entries(patches)
}

interface Beat {
  name?: string
  /** 0: no beat of its own (load-only). */
  intervalMs: number
  /** Runs right after every successful active load: partial and load-only sources, page tasks. */
  followsLoad: boolean
  /** The next run is the one right after an active load: a source re-sorts the page once its values land. */
  afterLoad: boolean
  run: (signal: AbortSignal, afterLoad: boolean) => Promise<void>
  timer: ReturnType<typeof setTimeout> | null
  controller: AbortController | null
  /** The run in flight, aborted or not: a beat never starts while it is set. */
  inflight: Promise<void> | null
  failures: number
  endedAt: number
  /** Wants a run: it starts as soon as nothing holds it back (the first load, an active load, the run in flight). */
  again: boolean
  /** That run was asked for by name: it goes ahead while paused or hidden. */
  forced: boolean
}

/** Edits of one field. A session lasts while edits are in flight; the latest one decides the outcome. */
interface FieldGuard {
  session: number
  /** Stamp of the latest edit; null when no edit is in flight. */
  lock: number | null
  /** Answers to requests stamped up to this may not write the field. */
  writableAfter: number
  /** The value the server last confirmed: the rollback target. */
  base: unknown
  /** The value the latest edit wrote. */
  value: unknown
  inflight: number
  failed: boolean
}

export function useLiveTable<T extends object>(options: LiveTableOptions<T>): LiveTable<T> {
  const { getId } = options
  const records = shallowReactive(new Map<string, T>())
  const ids = shallowRef<readonly LiveRowId[]>([])
  const total = ref(0)
  const loading = ref(false)
  const pending = ref(0)
  const pendingMore = ref(false)
  const gone = shallowReactive(new Set<string>())
  const paused = ref(false)
  const hidden = ref(pageHidden())
  const failing = ref(false)
  const loadFailed = ref(false)

  const rows = computed(() => ids.value.flatMap((id) => records.get(keyOf(id)) ?? []))
  const transitioning = computed(() => loading.value && ids.value.length > 0)
  const status = computed<LiveStatus>(() =>
    paused.value || hidden.value ? 'paused' : failing.value || loadFailed.value ? 'stale' : 'live')

  // ---- Records and edit guards ----
  // Every request and every edit takes a stamp; a field ignores answers to requests stamped up to its writableAfter.
  let stamp = 0
  const guards = new Map<string, Map<string, FieldGuard>>()
  /** The fields the last whole row of each record carried: only these can go missing from the next one. */
  const wholeFields = new Map<string, Set<string>>()
  /** Rows taken off by remove(), with the stamp of that moment: answers to requests sent before it may still carry them. */
  const removed = new Map<string, number>()
  /** The order of the page as the server sent it, for sortBy(null). */
  let served: LiveRowId[] = []
  let order: ((a: T, b: T) => number) | null = null

  function guardOf(key: string, field: string) {
    let byField = guards.get(key)
    if (!byField) guards.set(key, (byField = new Map()))
    let guard = byField.get(field)
    if (!guard) byField.set(field, (guard = { session: 0, lock: null, writableAfter: 0, base: undefined, value: undefined, inflight: 0, failed: false }))
    return guard
  }
  function writable(key: string, field: string, requestedAt: number) {
    const guard = guards.get(key)?.get(field)
    return !guard || (guard.lock === null && requestedAt > guard.writableAfter)
  }
  function assign(row: T, field: string, value: unknown) {
    const target = row as Record<string, unknown>
    if (!sameValue(target[field], value)) target[field] = value
  }
  function applyRow(key: string, values: Partial<T>, requestedAt: number, complete: boolean) {
    const row = records.get(key) as Record<string, unknown> | undefined
    if (!row) return
    const raw = toRaw(row)
    const id = getId(raw as T)
    // A write that would change the row's id (even only its type: 12 → '12') is skipped, so its key never changes.
    const keepsId = (field: string, value: unknown) => Object.is(getId({ ...raw, [field]: value } as T), id)
    for (const [field, value] of Object.entries(values)) {
      if (writable(key, field, requestedAt) && !sameValue(row[field], value) && keepsId(field, value)) row[field] = value
    }
    if (!complete) return
    const carried = new Set(Object.keys(values))
    for (const field of wholeFields.get(key) ?? []) {
      if (carried.has(field)) continue
      // A field under edit stays; it goes with the first whole row after the edit that still leaves it out.
      if (!writable(key, field, requestedAt)) carried.add(field)
      else if (keepsId(field, undefined)) delete row[field]
    }
    wholeFields.set(key, carried)
  }
  function applyPatches(patches: LivePatches<T>, requestedAt: number, complete: boolean) {
    for (const [id, values] of entriesOf(patches)) applyRow(keyOf(id), values, requestedAt, complete)
  }
  function upsert(row: T, requestedAt: number) {
    const key = keyOf(getId(row))
    if (records.has(key)) applyRow(key, row, requestedAt, true)
    else {
      records.set(key, reactive({ ...row }) as T)
      wholeFields.set(key, new Set(Object.keys(row)))
    }
  }
  /** Drops the rows remove() took off after this request was sent; forgets removals no later answer can carry. */
  function withoutRemoved(items: T[], requestedAt: number) {
    const kept = items.filter((row) => !((removed.get(keyOf(getId(row))) ?? 0) > requestedAt))
    for (const [key, at] of removed) if (at < requestedAt) removed.delete(key)
    return kept
  }
  /** The list sorted by the comparator; the list as it is when there is none or it throws. */
  const ordered = (list: LiveRowId[]) => {
    const compare = order
    if (!compare) return list
    try {
      return [...list].sort((a, b) => compare(records.get(keyOf(a))!, records.get(keyOf(b))!))
    } catch {
      return list
    }
  }
  /** The rows on the page in the server's order. */
  const servedOnPage = () => {
    const shown = new Set(ids.value.map(keyOf))
    return served.filter((id) => shown.has(keyOf(id)))
  }
  /** Shows these ids in this order; records, guards and dim marks of ids off the page go. */
  function show(next: LiveRowId[]) {
    const keep = new Set(next.map(keyOf))
    for (const key of [...records.keys()]) if (!keep.has(key)) records.delete(key)
    for (const key of [...guards.keys()]) if (!keep.has(key)) guards.delete(key)
    for (const key of [...wholeFields.keys()]) if (!keep.has(key)) wholeFields.delete(key)
    for (const key of [...gone]) if (!keep.has(key)) gone.delete(key)
    const current = ids.value
    if (current.length !== next.length || current.some((id, i) => keyOf(id) !== keyOf(next[i]))) ids.value = next
  }

  // ---- Beats ----
  const beats: Beat[] = []
  let started = false
  let disposed = false
  const running = () => started && !disposed && !paused.value && !hidden.value
  /** When the next run is due after the last one; null for a load-only beat that is not retrying a failure. */
  const delayOf = (beat: Beat) => (beat.failures ? Math.max(beat.intervalMs, backoff(beat.failures)) : beat.intervalMs || null)

  function clearTimer(beat: Beat) {
    if (beat.timer !== null) clearTimeout(beat.timer)
    beat.timer = null
  }
  function schedule(beat: Beat, delay: number | null) {
    clearTimer(beat)
    if (delay === null) return
    beat.timer = setTimeout(() => {
      beat.timer = null
      want(beat, false)
    }, delay)
  }
  function abortBeat(beat: Beat) {
    clearTimer(beat)
    beat.controller?.abort()
    beat.controller = null
  }
  /** Asks for a run; it starts now if nothing holds it back, else as soon as that is over. */
  function want(beat: Beat, forced: boolean) {
    beat.again = true
    if (forced) beat.forced = true
    drive(beat)
  }
  function drive(beat: Beat) {
    if (disposed || !beat.again || !started || loading.value || beat.inflight) return
    if (!beat.forced && !running()) return // catchUp() on visible / resume picks it up
    void start(beat)
  }
  async function start(beat: Beat) {
    beat.again = beat.forced = false
    clearTimer(beat)
    const controller = new AbortController()
    beat.controller = controller
    const afterLoad = beat.afterLoad
    const job = (async () => {
      try {
        await beat.run(controller.signal, afterLoad)
        if (!controller.signal.aborted) {
          beat.failures = 0
          beat.endedAt = Date.now()
          // Aborted or failed, the run that brings the values after a load is still owed (refresh, retry).
          if (afterLoad) beat.afterLoad = false
        }
      } catch (error) {
        if (!controller.signal.aborted && !isAbort(error)) beat.failures++
      }
    })()
    beat.inflight = job
    await job
    beat.inflight = null
    if (beat.controller === controller) beat.controller = null
    failing.value = beats.some((b) => b.failures > 0)
    if (disposed) return
    if (beat.again) return drive(beat)
    // Next run, unless an active load is on (it reschedules every beat when it ends) or one is lined up already. An
    // aborted run counts too when nothing is lined up: its load may have ended while the tab was hidden or the table
    // paused, and the catch-up skipped the beat because this run was still out.
    if (running() && !loading.value && (!controller.signal.aborted || beat.timer === null)) schedule(beat, delayOf(beat))
  }
  /** After an active load: run what asked to run (after a success: partial sources and tasks), schedule the rest. */
  function resumeBeats(succeeded: boolean) {
    for (const beat of beats) {
      if (succeeded && beat.followsLoad) beat.again = beat.afterLoad = true
      if (beat.again) drive(beat)
      else if (running()) schedule(beat, delayOf(beat))
    }
  }
  /** Visible, online or resumed: a failed load is retried at once; beats that ran within their beat just wait. */
  function catchUp() {
    if (!running()) return
    if (owed) {
      if (!loading.value) void load(false)
      return
    }
    if (loading.value) return // its end reschedules every beat
    const now = Date.now()
    for (const beat of beats) {
      if (beat.inflight) continue // its end schedules the next run
      const delay = delayOf(beat)
      if (beat.again || beat.failures > 0) want(beat, false)
      else if (delay === null) continue // load-only: nothing to catch up
      else if (beat.endedAt + delay <= now) want(beat, false)
      else schedule(beat, beat.endedAt + delay - now)
    }
  }
  const addBeat = (name: string | undefined, intervalMs: number, followsLoad: boolean, run: Beat['run']) => {
    const ms = beatMs(intervalMs)
    beats.push({
      name, intervalMs: ms, followsLoad: followsLoad || ms === 0, afterLoad: false, run,
      timer: null, controller: null, inflight: null, failures: 0, endedAt: 0, again: false, forced: false
    })
  }

  let generation = 0
  let active: AbortController | null = null
  /** The last active load failed; it is retried on the back-off beat. */
  let owed = false
  let loadFailures = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  const clearRetry = () => {
    if (retryTimer !== null) clearTimeout(retryTimer)
    retryTimer = null
  }

  for (const source of options.sources ?? []) {
    const complete = source.complete === true
    addBeat(source.name, source.intervalMs, !complete, async (signal, afterLoad) => {
      if (!ids.value.length) return
      const requestedAt = ++stamp
      const patches = await source.fetch([...ids.value], signal)
      // An active load or refresh(name) aborts the run; an answer that ignores the signal is still dropped.
      if (signal.aborted) return
      applyPatches(patches, requestedAt, complete)
      // The load sorted the page before these values were in; it is still that one sort the user asked for.
      if (afterLoad && order) show(ordered(servedOnPage()))
    })
  }
  for (const task of options.tasks ?? []) addBeat(task.name, task.intervalMs, true, (signal) => task.run(signal))

  const membershipMs = beatMs(options.membershipIntervalMs ?? DEFAULT_MEMBERSHIP_MS)
  if (membershipMs > 0) {
    addBeat(undefined, membershipMs, false, async (signal) => {
      // The page on screen is not the current query while a failed load is owed; its retry settles membership.
      if (owed) return
      const requestedAt = ++stamp
      const page = await options.fetchPage(signal)
      if (signal.aborted) return
      const items = withoutRemoved(page.items, requestedAt)
      total.value = page.total - (page.items.length - items.length)
      const shown = new Set(ids.value.map(keyOf))
      const added: T[] = []
      for (const row of items) {
        const key = keyOf(getId(row))
        if (shown.has(key)) applyRow(key, row, requestedAt, true)
        else added.push(row)
      }
      if (options.tail) {
        const size = options.tail.pageSize()
        if (options.tail.enabled()) {
          for (const row of added) upsert(row, requestedAt)
          served = items.map(getId).slice(0, size)
          show(served)
          pending.value = 0
          pendingMore.value = false
        } else {
          pending.value = added.length
          pendingMore.value = added.length >= size
        }
        return
      }
      const fresh = new Set(items.map((row) => keyOf(getId(row))))
      gone.clear()
      for (const id of ids.value) if (!fresh.has(keyOf(id))) gone.add(keyOf(id))
      pending.value = added.length + gone.size
    })
  }

  // ---- Active load ----
  async function load(fromCaller: boolean) {
    const at = ++generation
    active?.abort()
    clearRetry()
    const controller = new AbortController()
    active = controller
    const requestedAt = ++stamp
    // Answers still on their way were asked for the previous page.
    for (const beat of beats) abortBeat(beat)
    started = true
    loading.value = true
    let succeeded = false
    let failure: unknown = null
    try {
      const page = await options.fetchPage(controller.signal)
      if (at !== generation) return
      const items = withoutRemoved(page.items, requestedAt)
      for (const row of items) upsert(row, requestedAt)
      served = items.map(getId)
      show(ordered(served))
      total.value = page.total - (page.items.length - items.length)
      pending.value = 0
      pendingMore.value = false
      gone.clear()
      owed = false
      loadFailures = 0
      loadFailed.value = false
      succeeded = true
    } catch (error) {
      if (at !== generation || controller.signal.aborted || isAbort(error)) return
      owed = true
      loadFailures++
      loadFailed.value = true
      failure = error
    } finally {
      if (at === generation && !disposed) {
        loading.value = false
        active = null
        if (owed) {
          retryTimer = setTimeout(() => {
            retryTimer = null
            if (running() && !loading.value) void load(false)
          }, backoff(loadFailures))
        }
        resumeBeats(succeeded)
      }
    }
    if (failure && fromCaller) throw failure
  }

  // ---- Edits ----
  async function edit<K extends keyof T & string>(id: LiveRowId, field: K, value: T[K], commit: () => Promise<Partial<T> | void>) {
    const key = keyOf(id)
    const guard = guardOf(key, field)
    const mine = ++stamp
    const row = records.get(key)
    if (guard.lock === null) Object.assign(guard, { session: mine, base: row?.[field], inflight: 0, failed: false })
    const session = guard.session
    Object.assign(guard, { lock: mine, writableAfter: mine, value, failed: false })
    guard.inflight++
    if (row) assign(row, field, value)
    // The session is over when it settled or the row left the page (its guards went with it).
    const live = () => guard.session === session && guard.lock !== null && guards.get(key)?.get(field) === guard
    try {
      const result = await commit()
      if (live()) {
        guard.inflight--
        if (guard.lock === mine) settle(key, field, guard, result)
        else {
          // An older edit landed while a newer one is on its way: the server holds this value until then.
          guard.base = result && hasOwn(result, field) ? result[field] : value
          if (guard.failed && guard.inflight === 0) settle(key, field, guard)
        }
      }
      return result
    } catch (error) {
      if (live()) {
        guard.inflight--
        if (guard.lock === mine) guard.failed = true
        if (guard.failed && guard.inflight === 0) settle(key, field, guard)
      }
      throw error
    }
  }
  /** Ends an edit session: unlocks, then writes the latest edit's answer or rolls back to the confirmed value. */
  function settle(key: string, field: string, guard: FieldGuard, result?: Partial<T> | void) {
    guard.lock = null
    guard.writableAfter = ++stamp
    const row = records.get(key) as Record<string, unknown> | undefined
    if (!row) return
    if (guard.failed) {
      if (sameValue(row[field], guard.value)) row[field] = guard.base
    } else if (result) applyRow(key, result, ++stamp, false)
  }

  // ---- Page actions ----
  function remove(id: LiveRowId) {
    const key = keyOf(id)
    removed.set(key, ++stamp)
    const next = ids.value.filter((x) => keyOf(x) !== key)
    // A row the membership check found gone is already out of the server's total.
    const wasGone = gone.delete(key)
    if (wasGone) pending.value = Math.max(0, pending.value - 1)
    if (next.length !== ids.value.length) {
      ids.value = next
      if (!wasGone) total.value = Math.max(0, total.value - 1)
    }
    records.delete(key)
    guards.delete(key)
    wholeFields.delete(key)
  }
  function sortBy(compare: ((a: T, b: T) => number) | null) {
    order = compare
    show(ordered(servedOnPage()))
  }
  async function refresh(name: string) {
    await Promise.all(beats.filter((beat) => beat.name === name).map(async (beat) => {
      // An answer already on its way predates the change that asked for this refresh; its end starts the new run.
      beat.controller?.abort()
      const out = beat.inflight
      want(beat, true)
      if (!started || loading.value) return // remembered: runs when the load ends
      if (out) await out
      if (beat.inflight) await beat.inflight
    }))
  }
  function mergeRows(patches: LivePatches<T>, mergeOptions: { complete?: boolean } = {}) {
    applyPatches(patches, ++stamp, mergeOptions.complete === true)
  }
  function pause() {
    paused.value = true
    for (const beat of beats) clearTimer(beat)
  }
  function resume() {
    if (!paused.value) return
    paused.value = false
    catchUp()
  }

  useEventListener(document, 'visibilitychange', () => {
    hidden.value = pageHidden()
    if (hidden.value) for (const beat of beats) clearTimer(beat)
    else catchUp()
  })
  useEventListener(window, 'online', catchUp)

  onScopeDispose(() => {
    disposed = true
    generation++
    active?.abort()
    clearRetry()
    for (const beat of beats) abortBeat(beat)
  })

  const record = (id: LiveRowId) => records.get(keyOf(id))
  const isDimmed = (id: LiveRowId) => {
    const key = keyOf(id)
    if (gone.has(key)) return true
    const row = records.get(key)
    return !!row && !!options.matches && !options.matches(row)
  }

  return {
    rows,
    ids,
    total,
    loading,
    transitioning,
    status,
    pending,
    pendingMore,
    record,
    isDimmed,
    reload: () => load(true),
    applyPending: () => load(true),
    pause,
    resume,
    refresh,
    sortBy,
    remove,
    mergeRows,
    edit
  }
}
