import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, watch } from 'vue'

import { sameValue, useLiveTable, type LivePage, type LiveRowId, type LiveTableOptions } from '../useLiveTable'

interface Row {
  id: number
  name: string
  status: string
  tags: string[]
  extra: { note: string }
}

const row = (id: number, over: Partial<Row> = {}): Row => ({ id, name: `acc-${id}`, status: 'active', tags: ['a'], extra: { note: '' }, ...over })
const page = (...items: Row[]): LivePage<Row> => ({ items, total: items.length })

let hidden = false
const setHidden = (value: boolean) => {
  hidden = value
  document.dispatchEvent(new Event('visibilitychange'))
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

let scope = effectScope()
function engine(options: Partial<LiveTableOptions<Row>> & Pick<LiveTableOptions<Row>, 'fetchPage'>) {
  scope = effectScope()
  return scope.run(() => useLiveTable<Row>({ getId: (r) => r.id, membershipIntervalMs: 0, ...options }))!
}

beforeEach(() => {
  vi.useFakeTimers()
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
})
afterEach(() => {
  scope.stop()
  vi.useRealTimers()
})

describe('field-by-field merge', () => {
  it('keeps the row object and assigns only fields whose value changed; arrays and objects compare by value', async () => {
    let next: Row[] = [row(1)]
    const sources = [{ intervalMs: 3000, fetch: async () => next.map((r) => [r.id, r] as const) }]
    const table = engine({ fetchPage: async () => page(row(1)), sources })
    await table.reload()
    const first = table.rows.value[0]
    const seen = { name: 0, tags: 0, extra: 0, status: 0 }
    watch(() => first.name, () => seen.name++, { flush: 'sync' })
    watch(() => first.tags, () => seen.tags++, { flush: 'sync' })
    watch(() => first.extra, () => seen.extra++, { flush: 'sync' })
    watch(() => first.status, () => seen.status++, { flush: 'sync' })

    // New objects with the same values, and one changed field.
    next = [row(1, { status: 'error', tags: ['a'], extra: { note: '' } })]
    await vi.advanceTimersByTimeAsync(3000)
    expect(seen).toEqual({ name: 0, tags: 0, extra: 0, status: 1 })
    expect(table.rows.value[0]).toBe(first)
    expect(first.status).toBe('error')

    next = [row(1, { status: 'error', tags: ['a', 'b'] })]
    await vi.advanceTimersByTimeAsync(3000)
    expect(seen).toEqual({ name: 0, tags: 1, extra: 0, status: 1 })
    expect(first.tags).toEqual(['a', 'b'])
  })

  it('compares nested API values by value', () => {
    expect(sameValue({ a: [1, { b: null }] }, { a: [1, { b: null }] })).toBe(true)
    expect(sameValue({ a: 1 }, { a: 1, b: undefined })).toBe(false)
    expect(sameValue([1, 2], { 0: 1, 1: 2 })).toBe(false)
  })
})

describe('optimistic edits', () => {
  it('writes at once, keeps background answers off the locked field, and takes the server value on success', async () => {
    let server: Row = row(1, { name: 'old' })
    const sources = [{ intervalMs: 3000, fetch: async () => [[server.id, { ...server }] as const] }]
    const table = engine({ fetchPage: async () => page({ ...server }), sources })
    await table.reload()
    const save = deferred<Partial<Row>>()
    const done = table.edit(1, 'name', 'typed', () => save.promise)
    expect(table.record(1)!.name).toBe('typed')

    await vi.advanceTimersByTimeAsync(3000) // background still says "old"
    expect(table.record(1)!.name).toBe('typed')

    server = row(1, { name: 'saved' })
    save.resolve({ name: 'saved' })
    await done
    expect(table.record(1)!.name).toBe('saved')
    server = row(1, { name: 'renamed elsewhere' })
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.record(1)!.name).toBe('renamed elsewhere')
  })

  it('rolls back on failure only while the field still holds the optimistic value, and hands the error back', async () => {
    const table = engine({ fetchPage: async () => page(row(1, { name: 'old' })) })
    await table.reload()
    const failure = new Error('save failed')
    await expect(table.edit(1, 'name', 'typed', async () => { throw failure })).rejects.toBe(failure)
    expect(table.record(1)!.name).toBe('old')

    // A second edit takes over before the first one fails: the first one leaves the field alone.
    const first = deferred<void>()
    const firstDone = table.edit(1, 'name', 'first', () => first.promise)
    const secondDone = table.edit(1, 'name', 'second', async () => undefined)
    await secondDone
    first.reject(failure)
    await expect(firstDone).rejects.toBe(failure)
    expect(table.record(1)!.name).toBe('second')
  })

  it('drops an answer to a background request sent before the edit started', async () => {
    const answer = deferred<Array<readonly [LiveRowId, Partial<Row>]>>()
    const fetch = vi.fn(() => answer.promise)
    const table = engine({ fetchPage: async () => page(row(1, { name: 'old', status: 'active' })), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetch).toHaveBeenCalledTimes(1) // sent, not answered yet

    await table.edit(1, 'name', 'saved', async () => ({ name: 'saved' }))
    answer.resolve([[1, { name: 'old', status: 'disabled' }]])
    await vi.advanceTimersByTimeAsync(0)
    expect(table.record(1)!.name).toBe('saved')
    expect(table.record(1)!.status).toBe('disabled') // other fields of that answer still apply
  })
})

describe('frozen membership', () => {
  it('keeps the rows and their order between active loads, counts the changes and dims rows that went', async () => {
    let server = page(row(1), row(2), row(3))
    const table = engine({ fetchPage: async () => server, membershipIntervalMs: 30_000, matches: (r) => r.status === 'active' })
    await table.reload()
    server = page(row(4), row(2, { status: 'disabled' }), row(1))

    await vi.advanceTimersByTimeAsync(30_000)
    expect(table.ids.value).toEqual([1, 2, 3])
    expect(table.pending.value).toBe(2) // 4 appeared, 3 went
    expect(table.isDimmed(3)).toBe(true) // gone
    expect(table.isDimmed(2)).toBe(true) // no longer matches the filter
    expect(table.isDimmed(1)).toBe(false)
    expect(table.record(2)!.status).toBe('disabled')

    await table.applyPending()
    expect(table.ids.value).toEqual([4, 2, 1])
    expect(table.pending.value).toBe(0)
    expect(table.isDimmed(3)).toBe(false)
  })

  it('keeps old rows during an active load, flags the transition and drops a stale answer', async () => {
    const answers = [deferred<LivePage<Row>>(), deferred<LivePage<Row>>()]
    let call = 0
    const signals: AbortSignal[] = []
    const table = engine({ fetchPage: async (signal) => { signals.push(signal); return answers[call++].promise } })
    const firstLoad = table.reload()
    answers[0].resolve(page(row(1)))
    await firstLoad

    const slow = table.reload()
    expect(table.transitioning.value).toBe(true)
    expect(table.ids.value).toEqual([1])
    answers.push(deferred())
    const latest = table.reload() // a newer filter replaces the slow load
    expect(signals[1].aborted).toBe(true)
    answers[1].resolve(page(row(9)))
    answers[2].resolve(page(row(2)))
    await Promise.all([slow, latest])
    expect(table.ids.value).toEqual([2])
    expect(table.transitioning.value).toBe(false)
  })

  it('in log mode inserts new rows at the top and trims to the page size while following, otherwise only counts them', async () => {
    let follow = true
    let server = page(row(3), row(2), row(1))
    const table = engine({ fetchPage: async () => server, membershipIntervalMs: 5000, tail: { enabled: () => follow, pageSize: () => 3 } })
    await table.reload()

    server = page(row(5), row(4), row(3))
    await vi.advanceTimersByTimeAsync(5000)
    expect(table.ids.value).toEqual([5, 4, 3])
    expect(table.record(1)).toBeUndefined()

    follow = false
    server = page(row(7), row(6), row(5))
    await vi.advanceTimersByTimeAsync(5000)
    expect(table.ids.value).toEqual([5, 4, 3])
    expect(table.pending.value).toBe(2)
  })
})

describe('scheduling', () => {
  it('chains requests, pauses while the tab is hidden and catches up at once when it is visible again', async () => {
    const fetch = vi.fn(async () => [] as Array<readonly [LiveRowId, Partial<Row>]>)
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetch).toHaveBeenCalledTimes(1)

    setHidden(true)
    expect(table.status.value).toBe('paused')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetch).toHaveBeenCalledTimes(1)

    setHidden(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(table.status.value).toBe('live')
  })

  it('waits for a slow request before the next one, and aborts it when the scope goes', async () => {
    const answer = deferred<Array<readonly [LiveRowId, Partial<Row>]>>()
    const signals: AbortSignal[] = []
    const fetch = vi.fn((_ids: LiveRowId[], signal: AbortSignal) => { signals.push(signal); return answer.promise })
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(fetch).toHaveBeenCalledTimes(1)
    scope.stop()
    expect(signals[0].aborted).toBe(true)
  })

  it('backs off 3, 6, 12, 30 s after failures, shows stale, and goes live again after a success', async () => {
    let fail = true
    const fetch = vi.fn(async () => {
      if (fail) throw new Error('down')
      return [] as Array<readonly [LiveRowId, Partial<Row>]>
    })
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    const callsAt = async (ms: number) => {
      await vi.advanceTimersByTimeAsync(ms)
      return fetch.mock.calls.length
    }
    expect(await callsAt(3000)).toBe(1) // first beat fails
    expect(table.status.value).toBe('stale')
    expect(await callsAt(2999)).toBe(1)
    expect(await callsAt(1)).toBe(2) // +3 s
    expect(await callsAt(5999)).toBe(2)
    expect(await callsAt(1)).toBe(3) // +6 s
    expect(await callsAt(12_000)).toBe(4) // +12 s
    expect(await callsAt(29_999)).toBe(4)
    fail = false
    expect(await callsAt(1)).toBe(5) // +30 s
    expect(table.status.value).toBe('live')
    expect(await callsAt(3000)).toBe(6) // back to its own beat
  })

  it('pauses and resumes by hand, catching up on resume', async () => {
    const fetch = vi.fn(async () => [] as Array<readonly [LiveRowId, Partial<Row>]>)
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    table.pause()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(fetch).not.toHaveBeenCalled()
    expect(table.status.value).toBe('paused')
    table.resume()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
