/**
 * Live table engine: one store per resource for the tables that refresh in place (accounts, groups, users, usage).
 *
 * - Records are kept by id, the current page as an id list (`ids`); `rows` joins the two. Ids compare by their string
 *   form, so `12` and `'12'` are the same row. Usage records must use `source:id` as their id (`usage:12`,
 *   `unbilled:12`): the two id sequences overlap.
 * - A row object stays the same across refreshes and only fields whose value changed are assigned (arrays and objects
 *   compared by value; JSON data only). Whatever reads only unchanged fields is not re-run; a DataTable row re-renders
 *   as a whole when any field its slots read changes. Rows from reload(), the membership check, sources marked
 *   `complete` and mergeRows(…, { complete: true }) are whole rows, id field included: a field the answer leaves out is
 *   removed (the backend omits empty fields). Other sources only set the fields they carry.
 * - reload() is the active load (page, filter, sort, manual refresh): it replaces the id list and never clears the rows
 *   on screen (`transitioning` meanwhile); stale answers are dropped. A failed load turns `status` to 'stale' and is
 *   retried on the back-off beat; the caller of reload() also gets the error.
 * - Sources refresh the rows of the current page on their own beat without changing membership or order; tasks are
 *   page-level jobs on the same scheduler (they run with no rows too). refresh(name) runs one of them now. The
 *   membership check (default every 30 s) counts rows that appeared or went (`pending`) and dims gone rows in place;
 *   applyPending() takes them in. With `tail` (usage records) the page follows the server's order while
 *   `tail.enabled()` holds; otherwise new rows are only counted (`pendingMore` once a full page is waiting: "N+").
 * - Beats are setTimeout chains started after a request ends; a source never overlaps itself. They skip while an active
 *   load runs, stop while the tab is hidden or paused, catch up at once on visible / online / resume (unless they ran
 *   within their own beat), back off 3, 6, 12, 30 s after failures, and abort on dispose.
 * - edit() writes a value at once and locks the field: no background answer writes it, nor any answer to a request
 *   sent before the edit settled. The latest edit's answer is written when it succeeds; an older edit's answer never
 *   is. When the latest edit fails the field returns to the value the server last confirmed (if it still shows the
 *   optimistic value), and the error goes back to the caller. Order changes only through reload() and sortBy().
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

export interface LiveSource<T> {
  /** For refresh(name). */
  name?: string
  /** Pause between the end of one request and the next one, in ms. */
  intervalMs: number
  /** Every answer is a whole row: fields it leaves out are removed from the record. */
  complete?: boolean
  /** Fresh values for rows of the current page. Entries for rows not on the page are ignored. */
  fetch: (ids: LiveRowId[], signal: AbortSignal) => Promise<LivePatches<T>>
}

/** A page-level job on the same beat machinery (for example the stats cards); it writes its own state. */
export interface LiveTask {
  name?: string
  intervalMs: number
  run: (signal: AbortSignal) => Promise<void>
}

export interface LiveTableOptions<T extends object> {
  getId: (row: T) => LiveRowId
  /** The current page for the current filter and sort; used by reload() and the membership check. */
  fetchPage: (signal: AbortSignal) => Promise<LivePage<T>>
  sources?: LiveSource<T>[]
  tasks?: LiveTask[]
  /** Membership check beat in ms; 0 turns it off. Default 30 s. */
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
  /** Runs the named sources and tasks now; resolves when their answer is in (at once during an active load). */
  refresh: (name: string) => Promise<void>
  /** Re-orders the current page once (a local sort the user asked for); updates never re-order. */
  sortBy: (compare: (a: T, b: T) => number) => void
  /** Takes a row off the page at once (after a delete). */
  remove: (id: LiveRowId) => void
  /** Merges values from an action's answer; `complete` treats each value as a whole row. Locked fields are kept. */
  mergeRows: (patches: LivePatches<T>, options?: { complete?: boolean }) => void
  edit: <K extends keyof T & string>(id: LiveRowId, field: K, value: T[K], commit: () => Promise<Partial<T> | void>) => Promise<Partial<T> | void>
}

const BACKOFF_MS = [3000, 6000, 12000, 30000]
const DEFAULT_MEMBERSHIP_MS = 30_000

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
  intervalMs: number
  run: (signal: AbortSignal) => Promise<void>
  timer: ReturnType<typeof setTimeout> | null
  controller: AbortController | null
  /** The run in flight, aborted or not: a beat never starts while it is set. */
  inflight: Promise<void> | null
  failures: number
  endedAt: number
  /** Run again as soon as the current run or the active load ends. */
  again: boolean
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
    for (const [field, value] of Object.entries(values)) if (writable(key, field, requestedAt)) assign(row as T, field, value)
    if (!complete) return
    for (const field of Object.keys(row)) if (!hasOwn(values, field) && writable(key, field, requestedAt)) delete row[field]
  }
  function applyPatches(patches: LivePatches<T>, requestedAt: number, complete: boolean) {
    for (const [id, values] of entriesOf(patches)) applyRow(keyOf(id), values, requestedAt, complete)
  }
  function upsert(row: T, requestedAt: number) {
    const key = keyOf(getId(row))
    if (records.has(key)) applyRow(key, row, requestedAt, true)
    else records.set(key, reactive({ ...row }) as T)
  }
  /** Shows these ids in this order; records, guards and dim marks of ids off the page go. */
  function show(next: LiveRowId[]) {
    const keep = new Set(next.map(keyOf))
    for (const key of [...records.keys()]) if (!keep.has(key)) records.delete(key)
    for (const key of [...guards.keys()]) if (!keep.has(key)) guards.delete(key)
    for (const key of [...gone]) if (!keep.has(key)) gone.delete(key)
    const current = ids.value
    if (current.length !== next.length || current.some((id, i) => keyOf(id) !== keyOf(next[i]))) ids.value = next
  }

  // ---- Beats ----
  const beats: Beat[] = []
  let started = false
  let disposed = false
  const running = () => started && !disposed && !paused.value && !hidden.value
  const delayOf = (beat: Beat) => (beat.failures ? Math.max(beat.intervalMs, backoff(beat.failures)) : beat.intervalMs)

  function clearTimer(beat: Beat) {
    if (beat.timer !== null) clearTimeout(beat.timer)
    beat.timer = null
  }
  function schedule(beat: Beat, delay: number) {
    clearTimer(beat)
    beat.timer = setTimeout(() => {
      beat.timer = null
      void runBeat(beat)
    }, delay)
  }
  function abortBeat(beat: Beat) {
    clearTimer(beat)
    beat.controller?.abort()
    beat.controller = null
  }
  async function runBeat(beat: Beat, force = false): Promise<void> {
    if (disposed || !started || (!force && !running())) return
    // An active load reschedules every beat when it ends; a run asked for by name follows it.
    if (loading.value) {
      if (force) beat.again = true
      return
    }
    if (beat.inflight) {
      beat.again = true
      return
    }
    clearTimer(beat)
    const controller = new AbortController()
    beat.controller = controller
    const job = (async () => {
      try {
        await beat.run(controller.signal)
        if (!controller.signal.aborted) {
          beat.failures = 0
          beat.endedAt = Date.now()
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
    if (beat.again) {
      beat.again = false
      return runBeat(beat, force)
    }
    // An aborted run belongs to a reload or a dispose, which schedules (or not) on its own.
    if (!controller.signal.aborted && running()) schedule(beat, delayOf(beat))
  }
  /** After an active load: run what asked to run, schedule the rest. */
  function resumeBeats() {
    for (const beat of beats) {
      if (beat.again) {
        beat.again = false
        void runBeat(beat)
      } else if (running()) schedule(beat, delayOf(beat))
    }
  }
  /** Visible, online or resumed: a failed load is retried at once; beats that ran within their beat just wait. */
  function catchUp() {
    if (!running()) return
    if (owed) {
      if (!loading.value) void load(false)
      return
    }
    const now = Date.now()
    for (const beat of beats) {
      if (beat.inflight) continue
      const due = beat.endedAt + delayOf(beat) - now
      if (beat.failures > 0 || due <= 0) void runBeat(beat)
      else schedule(beat, due)
    }
  }
  const addBeat = (name: string | undefined, intervalMs: number, run: Beat['run']) =>
    beats.push({ name, intervalMs, run, timer: null, controller: null, inflight: null, failures: 0, endedAt: 0, again: false })

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
    addBeat(source.name, source.intervalMs, async (signal) => {
      if (!ids.value.length) return
      const requestedAt = ++stamp
      const patches = await source.fetch([...ids.value], signal)
      // An active load or refresh(name) aborts the run; an answer that ignores the signal is still dropped.
      if (!signal.aborted) applyPatches(patches, requestedAt, source.complete === true)
    })
  }
  for (const task of options.tasks ?? []) addBeat(task.name, task.intervalMs, task.run)

  const membershipMs = options.membershipIntervalMs ?? DEFAULT_MEMBERSHIP_MS
  if (membershipMs > 0) {
    addBeat(undefined, membershipMs, async (signal) => {
      // The page on screen is not the current query while a failed load is owed; its retry settles membership.
      if (owed) return
      const requestedAt = ++stamp
      const page = await options.fetchPage(signal)
      if (signal.aborted) return
      total.value = page.total
      const shown = new Set(ids.value.map(keyOf))
      const added: T[] = []
      for (const row of page.items) {
        const key = keyOf(getId(row))
        if (shown.has(key)) applyRow(key, row, requestedAt, true)
        else added.push(row)
      }
      if (options.tail) {
        const size = options.tail.pageSize()
        if (options.tail.enabled()) {
          for (const row of added) upsert(row, requestedAt)
          show(page.items.map(getId).slice(0, size))
          pending.value = 0
          pendingMore.value = false
        } else {
          pending.value = added.length
          pendingMore.value = added.length >= size
        }
        return
      }
      const fresh = new Set(page.items.map((row) => keyOf(getId(row))))
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
    let failure: unknown = null
    try {
      const page = await options.fetchPage(controller.signal)
      if (at !== generation) return
      for (const row of page.items) upsert(row, requestedAt)
      show(page.items.map(getId))
      total.value = page.total
      pending.value = 0
      pendingMore.value = false
      gone.clear()
      owed = false
      loadFailures = 0
      loadFailed.value = false
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
        resumeBeats()
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
  }
  function sortBy(compare: (a: T, b: T) => number) {
    show([...rows.value].sort(compare).map(getId))
  }
  async function refresh(name: string) {
    await Promise.all(beats.filter((beat) => beat.name === name).map(async (beat) => {
      // An answer already on its way predates the change that asked for this refresh.
      beat.controller?.abort()
      if (beat.inflight) await beat.inflight
      await runBeat(beat, true)
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
