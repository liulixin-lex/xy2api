/**
 * Live table engine: one store per resource for the tables that refresh in place (accounts, groups, users, usage).
 *
 * - Records are kept by id (`records`), the current page as an id list (`ids`); `rows` joins the two. A row object
 *   stays the same across refreshes and only fields whose value changed are assigned (arrays and objects compared by
 *   value), so a cell re-renders only when what it reads changed.
 * - reload() is the active load (page, filter, sort, manual refresh): it replaces the id list and never clears the
 *   rows on screen; `transitioning` is true meanwhile. Stale answers are dropped (AbortController + sequence).
 * - Sources are passive: each refreshes the rows of the current page on its own beat without changing membership or
 *   order. The membership check (default every 30 s) counts rows that appeared or went (`pending`) and marks gone
 *   rows dimmed in place; applyPending() takes them in. With `tail` (usage records) new rows go straight to the top
 *   while `tail.enabled()` holds, otherwise they are only counted.
 * - Beats are setTimeout chains started after a request ends; they stop while the tab is hidden or paused, catch up at
 *   once on visible / online / resume, back off 3, 6, 12, 30 s after failures (`status` 'stale'), and abort on dispose.
 * - edit() writes a value at once and locks the field: background answers do not overwrite it, nor do answers to
 *   requests sent before the edit settled. Success writes the server's values; failure rolls back only if the field
 *   still holds the optimistic value, then rethrows for the caller to report.
 *
 *   const table = useLiveTable<Account>({
 *     getId: (row) => row.id,
 *     fetchPage: (signal) => adminAPI.accounts.list(page.value, size.value, filters, { signal }),
 *     sources: [{ intervalMs: 3000, fetch: (ids, signal) => fetchRows(ids, signal) }]
 *   })
 *   await table.reload()   // first load and every page / filter / sort change
 *   <DataTable :data="table.rows.value" :loading="table.loading.value" />
 */
import { computed, onScopeDispose, reactive, ref, shallowReactive, shallowRef, toRaw, type ComputedRef, type Ref } from 'vue'
import { useEventListener } from '@vueuse/core'

export type LiveRowId = string | number
export type LiveStatus = 'live' | 'paused' | 'stale'
/** Field values by row id; a Map or an array of [id, values] pairs. */
export type LivePatches<T> = Iterable<readonly [LiveRowId, Partial<T>]>

export interface LivePage<T> {
  items: T[]
  total: number
}

export interface LiveSource<T> {
  /** Pause between the end of one request and the next one, in ms. */
  intervalMs: number
  /** Fresh values for rows of the current page. Entries for rows not on the page are ignored. */
  fetch: (ids: LiveRowId[], signal: AbortSignal) => Promise<LivePatches<T>>
}

export interface LiveTableOptions<T extends object> {
  getId: (row: T) => LiveRowId
  /** The current page for the current filter and sort; used by reload() and the membership check. */
  fetchPage: (signal: AbortSignal) => Promise<LivePage<T>>
  sources?: LiveSource<T>[]
  /** Membership check beat in ms; 0 turns it off. Default 30 s. */
  membershipIntervalMs?: number
  /** A row on the page that no longer matches the filter is dimmed in place. */
  matches?: (row: T) => boolean
  /** Log mode: new rows are inserted at the top while `enabled()` holds (page 1, newest first, scrolled to top). */
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
  record: (id: LiveRowId) => T | undefined
  isDimmed: (id: LiveRowId) => boolean
  reload: () => Promise<void>
  applyPending: () => Promise<void>
  pause: () => void
  resume: () => void
  /** Merges values from an action's answer into the rows; locked fields are kept. */
  mergeRows: (patches: LivePatches<T>) => void
  edit: <K extends keyof T & string>(id: LiveRowId, field: K, value: T[K], commit: () => Promise<Partial<T> | void>) => Promise<Partial<T> | void>
}

const BACKOFF_MS = [3000, 6000, 12000, 30000]
const DEFAULT_MEMBERSHIP_MS = 30_000

const pageHidden = () => typeof document !== 'undefined' && document.hidden
const isAbort = (error: unknown) => {
  const e = error as { name?: string; code?: string } | null
  return e?.name === 'AbortError' || e?.name === 'CanceledError' || e?.code === 'ERR_CANCELED'
}

/** Value equality for API data: primitives, arrays and plain objects. */
export function sameValue(a: unknown, b: unknown): boolean {
  const x = toRaw(a)
  const y = toRaw(b)
  if (Object.is(x, y)) return true
  if (typeof x !== 'object' || typeof y !== 'object' || x === null || y === null) return false
  if (Array.isArray(x) || Array.isArray(y)) {
    return Array.isArray(x) && Array.isArray(y) && x.length === y.length && x.every((item, i) => sameValue(item, y[i]))
  }
  const left = x as Record<string, unknown>
  const right = y as Record<string, unknown>
  const keys = Object.keys(left)
  return keys.length === Object.keys(right).length && keys.every((key) => Object.prototype.hasOwnProperty.call(right, key) && sameValue(left[key], right[key]))
}

interface Beat {
  intervalMs: number
  run: (signal: AbortSignal) => Promise<void>
  timer: ReturnType<typeof setTimeout> | null
  controller: AbortController | null
  failures: number
}

/** Edit state of one field: the edit holding the lock, and the stamp before which answers may not write it. */
interface FieldGuard {
  lock: number | null
  writableAfter: number
}

export function useLiveTable<T extends object>(options: LiveTableOptions<T>): LiveTable<T> {
  const { getId } = options
  const records = shallowReactive(new Map<LiveRowId, T>())
  const ids = shallowRef<readonly LiveRowId[]>([])
  const total = ref(0)
  const loading = ref(false)
  const pending = ref(0)
  const gone = shallowReactive(new Set<LiveRowId>())
  const paused = ref(false)
  const hidden = ref(pageHidden())
  const stale = ref(false)

  const rows = computed(() => ids.value.flatMap((id) => records.get(id) ?? []))
  const transitioning = computed(() => loading.value && ids.value.length > 0)
  const status = computed<LiveStatus>(() => (paused.value || hidden.value ? 'paused' : stale.value ? 'stale' : 'live'))

  // Every request and every edit takes a stamp; a field edited at stamp s ignores answers to requests stamped <= s.
  let stamp = 0
  const guards = new Map<LiveRowId, Map<string, FieldGuard>>()
  const guardOf = (id: LiveRowId, field: string) => {
    let byField = guards.get(id)
    if (!byField) guards.set(id, (byField = new Map()))
    let guard = byField.get(field)
    if (!guard) byField.set(field, (guard = { lock: null, writableAfter: 0 }))
    return guard
  }
  const writable = (id: LiveRowId, field: string, requestedAt: number) => {
    const guard = guards.get(id)?.get(field)
    return !guard || (guard.lock === null && requestedAt > guard.writableAfter)
  }

  function assign(row: T, field: string, value: unknown) {
    const target = row as Record<string, unknown>
    if (!sameValue(target[field], value)) target[field] = value
  }
  function applyPatch(id: LiveRowId, patch: Partial<T>, requestedAt: number) {
    const row = records.get(id)
    if (!row) return
    for (const [field, value] of Object.entries(patch)) if (writable(id, field, requestedAt)) assign(row, field, value)
  }
  function upsert(row: T, requestedAt: number) {
    const id = getId(row)
    if (records.has(id)) applyPatch(id, row, requestedAt)
    else records.set(id, reactive({ ...row }) as T)
  }
  function show(next: LiveRowId[]) {
    const keep = new Set(next)
    for (const id of [...records.keys()]) {
      if (keep.has(id)) continue
      records.delete(id)
      guards.delete(id)
    }
    ids.value = next
  }

  // ---- Beats ----
  const beats: Beat[] = []
  let started = false
  let disposed = false
  const running = () => started && !disposed && !paused.value && !hidden.value

  function halt(beat: Beat, abort: boolean) {
    if (beat.timer !== null) clearTimeout(beat.timer)
    beat.timer = null
    if (abort) {
      beat.controller?.abort()
      beat.controller = null
    }
  }
  function schedule(beat: Beat, delay: number) {
    halt(beat, false)
    beat.timer = setTimeout(() => {
      beat.timer = null
      void runBeat(beat)
    }, delay)
  }
  async function runBeat(beat: Beat) {
    if (!running() || beat.controller) return
    const controller = new AbortController()
    beat.controller = controller
    try {
      await beat.run(controller.signal)
      beat.failures = 0
    } catch (error) {
      if (!controller.signal.aborted && !isAbort(error)) beat.failures++
    } finally {
      if (beat.controller === controller) beat.controller = null
    }
    // An aborted run belongs to a reload or a dispose, which schedules (or not) on its own.
    if (controller.signal.aborted) return
    stale.value = beats.some((b) => b.failures > 0)
    if (!running()) return
    const backoff = beat.failures ? BACKOFF_MS[Math.min(beat.failures, BACKOFF_MS.length) - 1] : 0
    schedule(beat, Math.max(beat.intervalMs, backoff))
  }
  /** Immediate silent pull for every beat that is not already running. */
  function catchUp() {
    for (const beat of beats) {
      halt(beat, false)
      void runBeat(beat)
    }
  }
  const addBeat = (intervalMs: number, run: Beat['run']) => beats.push({ intervalMs, run, timer: null, controller: null, failures: 0 })

  let generation = 0

  for (const source of options.sources ?? []) {
    addBeat(source.intervalMs, async (signal) => {
      const requestedAt = ++stamp
      const at = generation
      if (!ids.value.length) return
      const patches = await source.fetch([...ids.value], signal)
      if (at !== generation) return
      for (const [id, patch] of patches) applyPatch(id, patch, requestedAt)
    })
  }

  const membershipMs = options.membershipIntervalMs ?? DEFAULT_MEMBERSHIP_MS
  if (membershipMs > 0) {
    addBeat(membershipMs, async (signal) => {
      const requestedAt = ++stamp
      const at = generation
      const page = await options.fetchPage(signal)
      if (at !== generation) return
      total.value = page.total
      const shown = new Set(ids.value)
      const added = page.items.filter((row) => !shown.has(getId(row)))
      for (const row of page.items) if (shown.has(getId(row))) applyPatch(getId(row), row, requestedAt)
      if (options.tail) {
        const follow = options.tail.enabled()
        if (follow && added.length) {
          for (const row of added) upsert(row, requestedAt)
          show([...added.map(getId), ...ids.value].slice(0, options.tail.pageSize()))
        }
        pending.value = follow ? 0 : added.length
        return
      }
      const fresh = new Set(page.items.map(getId))
      gone.clear()
      for (const id of ids.value) if (!fresh.has(id)) gone.add(id)
      pending.value = added.length + gone.size
    })
  }

  // ---- Active load ----
  let active: AbortController | null = null
  async function reload() {
    const at = ++generation
    active?.abort()
    const controller = new AbortController()
    active = controller
    const requestedAt = ++stamp
    // Answers still on their way were asked for the previous page.
    for (const beat of beats) halt(beat, true)
    loading.value = true
    try {
      const page = await options.fetchPage(controller.signal)
      if (at !== generation) return
      for (const row of page.items) upsert(row, requestedAt)
      show(page.items.map(getId))
      total.value = page.total
      pending.value = 0
      gone.clear()
      started = true
    } catch (error) {
      if (at !== generation || controller.signal.aborted || isAbort(error)) return
      throw error
    } finally {
      if (at === generation) {
        loading.value = false
        active = null
        if (running()) for (const beat of beats) schedule(beat, beat.intervalMs)
      }
    }
  }

  // ---- Edit protection ----
  async function edit<K extends keyof T & string>(id: LiveRowId, field: K, value: T[K], commit: () => Promise<Partial<T> | void>) {
    const row = records.get(id)
    const guard = guardOf(id, field)
    const mine = ++stamp
    const previous = row?.[field]
    guard.lock = mine
    guard.writableAfter = mine
    if (row) assign(row, field, value)
    const release = () => {
      if (guard.lock !== mine) return false
      guard.lock = null
      guard.writableAfter = ++stamp
      return true
    }
    try {
      const result = await commit()
      release()
      if (result) applyPatch(id, result, ++stamp)
      return result
    } catch (error) {
      if (release() && row && sameValue(row[field], value)) (row as Record<string, unknown>)[field] = previous
      throw error
    }
  }

  function mergeRows(patches: LivePatches<T>) {
    const requestedAt = ++stamp
    for (const [id, patch] of patches) applyPatch(id, patch, requestedAt)
  }

  function pause() {
    paused.value = true
    for (const beat of beats) halt(beat, false)
  }
  function resume() {
    if (!paused.value) return
    paused.value = false
    catchUp()
  }

  useEventListener(document, 'visibilitychange', () => {
    hidden.value = pageHidden()
    if (hidden.value) for (const beat of beats) halt(beat, false)
    else catchUp()
  })
  useEventListener(window, 'online', catchUp)

  onScopeDispose(() => {
    disposed = true
    generation++
    active?.abort()
    for (const beat of beats) halt(beat, true)
  })

  const record = (id: LiveRowId) => records.get(id)
  const isDimmed = (id: LiveRowId) => {
    if (gone.has(id)) return true
    const row = records.get(id)
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
    record,
    isDimmed,
    reload,
    applyPending: reload,
    pause,
    resume,
    mergeRows,
    edit
  }
}
