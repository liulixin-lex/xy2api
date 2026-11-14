import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, watch } from 'vue'

import { useLiveTable, type LivePage, type LivePatches, type LiveRowId, type LiveTableOptions } from '../useLiveTable'

interface Row {
  id: number
  name: string
  status: string
  tags: string[]
  extra: { note: string }
  error?: string
  seen?: Date
  today?: number
  usage?: { today: number }
}

const row = (id: number, over: Partial<Row> = {}): Row => ({ id, name: `acc-${id}`, status: 'active', tags: ['a'], extra: { note: '' }, ...over })
const page = (...items: Row[]): LivePage<Row> => ({ items, total: items.length })
const none = async (): Promise<LivePatches<Row>> => []

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

  it('reassigns a Date only when its time changed', async () => {
    let seen = new Date(1000)
    const table = engine({ fetchPage: async () => page(row(1, { seen })), sources: [{ intervalMs: 3000, fetch: async () => [[1, { seen }]] }] })
    await table.reload()
    let changes = 0
    watch(() => table.record(1)!.seen, () => changes++, { flush: 'sync' })
    seen = new Date(1000)
    await vi.advanceTimersByTimeAsync(3000)
    expect(changes).toBe(0)
    seen = new Date(2000)
    await vi.advanceTimersByTimeAsync(3000)
    expect(changes).toBe(1)
  })

  it('removes the fields a whole row leaves out, except a field under edit; partial answers only set what they carry', async () => {
    let whole = row(1, { error: 'boom' })
    const table = engine({
      fetchPage: async () => page({ ...whole }),
      sources: [
        { intervalMs: 3000, complete: true, fetch: async () => [[1, { ...whole }]] },
        { intervalMs: 5000, fetch: async () => [[1, { status: 'disabled' }]] }
      ]
    })
    await table.reload()
    await vi.advanceTimersByTimeAsync(5000) // whole row at 3 s, partial at 5 s
    expect(table.record(1)).toMatchObject({ error: 'boom', status: 'disabled' })

    whole = row(1) // the backend omits the empty field
    await vi.advanceTimersByTimeAsync(1000)
    expect('error' in table.record(1)!).toBe(false)
    expect(table.record(1)!.status).toBe('active')

    const save = deferred<void>()
    void table.edit(1, 'error', 'typed', () => save.promise)
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.record(1)!.error).toBe('typed')
    save.resolve()
    await vi.advanceTimersByTimeAsync(0)

    whole = row(1, { error: 'again' })
    await table.reload()
    whole = row(1)
    await table.reload() // an active load is whole rows too
    expect('error' in table.record(1)!).toBe(false)
    table.mergeRows([[1, { ...row(1, { name: 'merged' }), error: 'x' }]], { complete: true })
    table.mergeRows([[1, { ...row(1), name: 'merged' }]], { complete: true })
    expect('error' in table.record(1)!).toBe(false)
  })

  it('keeps fields only partial sources write; drops a whole-row field once it goes missing, after its edit is over', async () => {
    let whole = row(1, { error: 'boom' })
    const today = vi.fn((): Promise<LivePatches<Row>> => (today.mock.calls.length === 1 ? Promise.resolve([[1, { today: 42 }]]) : deferred<LivePatches<Row>>().promise))
    const table = engine({
      fetchPage: async () => page({ ...whole }),
      membershipIntervalMs: 30_000,
      sources: [
        { intervalMs: 3000, complete: true, fetch: async () => [[1, { ...whole }]] },
        { intervalMs: 5000, fetch: today } // answers once, then hangs
      ]
    })
    await table.reload()
    const save = deferred<void>()
    void table.edit(1, 'error', 'typed', () => save.promise)
    whole = row(1)
    await vi.advanceTimersByTimeAsync(31_000) // whole rows every 3 s and the membership check at 30 s
    expect(table.record(1)).toMatchObject({ today: 42, error: 'typed' })

    save.resolve()
    await vi.advanceTimersByTimeAsync(3000)
    expect('error' in table.record(1)!).toBe(false)
    await table.reload()
    expect(table.record(1)!.today).toBe(42)
  })

  it('never changes a row\'s id, not even its type', async () => {
    const answer = { ...row(12, { status: 'error' }), id: '12' as unknown as number }
    const table = engine({ fetchPage: async () => page(row(12)), sources: [{ intervalMs: 3000, complete: true, fetch: async () => ({ '12': answer }) }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.record(12)).toMatchObject({ id: 12, status: 'error' })
  })

  it('treats 12 and "12" as the same row', async () => {
    const table = engine({ fetchPage: async () => page(row(12)), sources: [{ intervalMs: 3000, fetch: async () => ({ '12': { status: 'error' } }) }] })
    await table.reload()
    expect(table.record('12')).toBe(table.record(12))
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.record(12)!.status).toBe('error')
    table.mergeRows([['12', { name: 'renamed' }]])
    expect(table.rows.value[0].name).toBe('renamed')
    table.remove('12')
    expect(table.ids.value).toEqual([])
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

  it('never writes a superseded edit\'s answer, and rolls back to the saved value when the latest edit fails', async () => {
    const table = engine({ fetchPage: async () => page(row(1, { name: 'saved' })) })
    await table.reload()
    const name = () => table.record(1)!.name
    const failure = new Error('save failed')
    const saves = Array.from({ length: 8 }, () => deferred<Partial<Row>>())
    const edit = (value: string, i: number) => table.edit(1, 'name', value, () => saves[i].promise)

    // The old edit answers after the new one.
    const [a, b] = [edit('a', 0), edit('b', 1)]
    saves[1].resolve({ name: 'b' })
    await b
    saves[0].resolve({ name: 'a' })
    await a
    expect(name()).toBe('b')

    // The old edit answers while the new one is still open.
    const [c, d] = [edit('c', 2), edit('d', 3)]
    saves[2].resolve({ name: 'c' })
    await c
    expect(name()).toBe('d')
    saves[3].resolve({ name: 'd' })
    await d
    expect(name()).toBe('d')

    // Both fail: back to the value saved before either.
    const [e, f] = [edit('e', 4), edit('f', 5)]
    saves[5].reject(failure)
    await expect(f).rejects.toBe(failure)
    expect(name()).toBe('f') // the older one may still land
    saves[4].reject(failure)
    await expect(e).rejects.toBe(failure)
    expect(name()).toBe('d')

    // The old one lands, then the new one fails: the server holds the old one's value.
    const [g, h] = [edit('g', 6), edit('h', 7)]
    saves[6].resolve({ name: 'g' })
    await g
    saves[7].reject(failure)
    await expect(h).rejects.toBe(failure)
    expect(name()).toBe('g')
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
    expect(table.pending.value).toBe(1) // 4 appeared and pushed 3 off: one change (spec 7e-11)
    expect(table.isDimmed(3)).toBe(true) // gone
    expect(table.isDimmed(2)).toBe(true) // no longer matches the filter
    expect(table.isDimmed(1)).toBe(false)
    expect(table.record(2)!.status).toBe('disabled')

    await table.applyPending()
    expect(table.ids.value).toEqual([4, 2, 1])
    expect(table.pending.value).toBe(0)
    expect(table.isDimmed(3)).toBe(false)
  })

  // 7e-11: a new account counted as "2 changes" (the new row plus the one it pushed off the page).
  it('counts the new rows when some appeared, else the rows that went', async () => {
    let server = page(row(1), row(2), row(3))
    const table = engine({ fetchPage: async () => server, membershipIntervalMs: 30_000 })
    await table.reload()
    server = page(row(1))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(table.pending.value).toBe(2) // 2 and 3 went, nothing new
    table.remove(2)
    expect(table.pending.value).toBe(1)
    server = page(row(5), row(6), row(1))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(table.pending.value).toBe(2) // 5 and 6 appeared; 3 went with them
  })

  it('takes a removed row off the page at once, with its count', async () => {
    let server: LivePage<Row> = { items: [row(1), row(2), row(3)], total: 10 }
    const table = engine({ fetchPage: async () => server, membershipIntervalMs: 30_000 })
    await table.reload()
    table.remove(2)
    expect(table.ids.value).toEqual([1, 3])
    expect(table.record(2)).toBeUndefined()
    expect(table.total.value).toBe(9)

    server = { items: [row(1), row(4)], total: 8 } // 3 went elsewhere, 4 appeared
    await vi.advanceTimersByTimeAsync(30_000)
    expect(table.pending.value).toBe(1) // the new row is the change
    table.remove(3) // already out of the server's total
    expect(table.ids.value).toEqual([1])
    expect(table.pending.value).toBe(1)
    expect(table.total.value).toBe(8)
    expect(table.isDimmed(3)).toBe(false)
  })

  it('keeps the id list when a reload answers the same page, and re-orders only when asked', async () => {
    let names = ['b', 'a', 'c']
    const current = () => page(...names.map((name, i) => row(i + 1, { name })))
    const table = engine({ fetchPage: async () => current(), sources: [{ intervalMs: 3000, complete: true, fetch: async () => current().items.map((r) => [r.id, r] as const) }] })
    await table.reload()
    const list = table.ids.value
    await table.reload()
    expect(table.ids.value).toBe(list)

    table.sortBy((x, y) => x.name.localeCompare(y.name))
    expect(table.ids.value).toEqual([2, 1, 3])
    names = ['b', 'z', 'c']
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.record(2)!.name).toBe('z')
    expect(table.ids.value).toEqual([2, 1, 3])
  })

  it('keeps a local sort through the engine\'s own retry load; sortBy(null) restores the server\'s order', async () => {
    let fail = false
    const table = engine({
      fetchPage: async () => {
        if (fail) throw new Error('down')
        return page(row(1, { name: 'b' }), row(2, { name: 'a' }), row(3, { name: 'c' }))
      }
    })
    await table.reload()
    table.sortBy((x, y) => x.name.localeCompare(y.name))
    fail = true
    await expect(table.reload()).rejects.toThrow('down')
    fail = false
    await vi.advanceTimersByTimeAsync(3000) // the engine retries on its own
    expect(table.ids.value).toEqual([2, 1, 3])
    table.sortBy(null)
    expect(table.ids.value).toEqual([1, 2, 3])
  })

  it('keeps a load whose new rows make the comparator throw, in the server\'s order; sortBy(null) shows that page', async () => {
    let current = page(row(1, { usage: { today: 1 } }), row(2, { usage: { today: 2 } }))
    const table = engine({ fetchPage: async () => current })
    await table.reload()
    table.sortBy((a, b) => b.usage!.today - a.usage!.today)
    expect(table.ids.value).toEqual([2, 1])
    current = page(row(3), row(4)) // no usage yet
    const logged = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    await table.reload()
    expect([table.ids.value, table.status.value]).toEqual([[3, 4], 'live'])
    expect(logged).toHaveBeenCalled() // development builds say why
    logged.mockRestore()
    table.sortBy(null)
    expect(table.ids.value).toEqual([3, 4])
  })

  it('re-sorts once when a partial source\'s values land after a page change, never on its later beats', async () => {
    let current = page(row(1), row(2))
    let usageOf = (id: LiveRowId) => Number(id)
    const fetch = vi.fn(async (ids: LiveRowId[]): Promise<LivePatches<Row>> => ids.map((id) => [id, { usage: { today: usageOf(id) } }] as const))
    const table = engine({ fetchPage: async () => current, sources: [{ intervalMs: 30_000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(0)
    table.sortBy((a, b) => (b.usage?.today ?? 0) - (a.usage?.today ?? 0))
    expect(table.ids.value).toEqual([2, 1])

    current = page(row(3), row(4)) // sorted before their usage is in
    await table.reload()
    await vi.advanceTimersByTimeAsync(0)
    expect(table.ids.value).toEqual([4, 3])

    usageOf = (id) => (id === 3 ? 100 : Number(id))
    await vi.advanceTimersByTimeAsync(30_000) // its own beat
    expect(table.record(3)!.usage!.today).toBe(100)
    expect(table.ids.value).toEqual([4, 3])
  })

  it('does not re-sort when the run after a load starts more than 10 s after it', async () => {
    let current = page(row(1), row(2))
    const fetch = vi.fn(async (ids: LiveRowId[]): Promise<LivePatches<Row>> => ids.map((id) => [id, { usage: { today: Number(id) } }] as const))
    const table = engine({ fetchPage: async () => current, sources: [{ intervalMs: 30_000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(0)
    table.sortBy((a, b) => (b.usage?.today ?? 0) - (a.usage?.today ?? 0))
    current = page(row(3), row(4))
    table.pause() // a dialog is open: the run after the load waits
    await table.reload()
    await vi.advanceTimersByTimeAsync(11_000)
    table.resume()
    await vi.advanceTimersByTimeAsync(0)
    expect(table.record(4)!.usage!.today).toBe(4) // the values landed
    expect(table.ids.value).toEqual([3, 4]) // without re-ordering the page
  })

  it('does not bring back a removed row from a membership check sent before the removal', async () => {
    const answer = deferred<LivePage<Row>>()
    const fetchPage = vi.fn(() => (fetchPage.mock.calls.length === 2 ? answer.promise : Promise.resolve(page(row(1), row(2)))))
    const table = engine({ fetchPage, membershipIntervalMs: 30_000 })
    await table.reload()
    await vi.advanceTimersByTimeAsync(30_000) // the check is out; the server still has row 2
    table.remove(2)
    answer.resolve(page(row(1), row(2)))
    await vi.advanceTimersByTimeAsync(0)
    expect([table.ids.value, table.pending.value, table.total.value]).toEqual([[1], 0, 1])
  })

  it('does not bring back a removed row in log mode either', async () => {
    const answer = deferred<LivePage<Row>>()
    const fetchPage = vi.fn(() => (fetchPage.mock.calls.length === 2 ? answer.promise : Promise.resolve(page(row(2), row(1)))))
    const table = engine({ fetchPage, membershipIntervalMs: 5000, tail: { enabled: () => true, pageSize: () => 20 } })
    await table.reload()
    await vi.advanceTimersByTimeAsync(5000)
    table.remove(2)
    answer.resolve(page(row(3), row(2), row(1)))
    await vi.advanceTimersByTimeAsync(0)
    expect(table.ids.value).toEqual([3, 1])
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

  it('in log mode follows the server\'s page while enabled, otherwise counts new rows and flags a full page as "N+"', async () => {
    let follow = true
    let server = page(row(3), row(2), row(1))
    const table = engine({ fetchPage: async () => server, membershipIntervalMs: 5000, tail: { enabled: () => follow, pageSize: () => 3 } })
    await table.reload()

    server = page(row(5), row(3), row(4), row(2)) // the server's order, one row over the page size
    await vi.advanceTimersByTimeAsync(5000)
    expect(table.ids.value).toEqual([5, 3, 4])
    expect(table.record(1)).toBeUndefined()

    follow = false
    server = page(row(7), row(6), row(5))
    await vi.advanceTimersByTimeAsync(5000)
    expect(table.ids.value).toEqual([5, 3, 4])
    expect([table.pending.value, table.pendingMore.value]).toEqual([2, false])

    server = page(row(10), row(9), row(8))
    await vi.advanceTimersByTimeAsync(5000)
    expect([table.pending.value, table.pendingMore.value]).toEqual([3, true])
    await table.applyPending()
    expect(table.ids.value).toEqual([10, 9, 8])
    expect([table.pending.value, table.pendingMore.value]).toEqual([0, false])
  })
})

describe('scheduling', () => {
  it('chains requests, pauses while the tab is hidden and catches up at once when it is visible again', async () => {
    const fetch = vi.fn(async () => [] as Array<readonly [LiveRowId, Partial<Row>]>)
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, complete: true, fetch }] })
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
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, complete: true, fetch }] })
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

  it('does not re-pull on visible when a source ran within its own beat', async () => {
    const fetch = vi.fn(none)
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 60_000, complete: true, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(60_000)
    setHidden(true)
    await vi.advanceTimersByTimeAsync(10_000)
    setHidden(false)
    await vi.advanceTimersByTimeAsync(49_999)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('runs every source on its own beat: a slow or failing one does not hold the others back', async () => {
    const slow = vi.fn(() => deferred<LivePatches<Row>>().promise)
    const failing = vi.fn(async (): Promise<LivePatches<Row>> => { throw new Error('down') })
    const healthy = vi.fn(async (): Promise<LivePatches<Row>> => [[1, { status: 'error' }]])
    const table = engine({ fetchPage: async () => page(row(1)), sources: [slow, failing, healthy].map((fetch) => ({ intervalMs: 3000, fetch })) })
    await table.reload()
    await vi.advanceTimersByTimeAsync(9000)
    // Each runs right after the load; failing: then +3 s, then +6 s.
    expect([slow, failing, healthy].map((f) => f.mock.calls.length)).toEqual([1, 3, 4])
    expect(table.record(1)!.status).toBe('error')
    expect(table.status.value).toBe('stale')
  })

  it('catches up at once when the network is back', async () => {
    let fail = true
    const fetch = vi.fn(async (): Promise<LivePatches<Row>> => {
      if (fail) throw new Error('offline')
      return []
    })
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, complete: true, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000)
    expect(table.status.value).toBe('stale')
    fail = false
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(table.status.value).toBe('live')
  })

  it('skips source beats during an active load and drops source answers asked for the page before it', async () => {
    const early = deferred<LivePatches<Row>>()
    const fetch = vi.fn((_ids: LiveRowId[], _signal: AbortSignal) => (fetch.mock.calls.length === 1 ? early.promise : none()))
    const next = deferred<LivePage<Row>>()
    const fetchPage = vi.fn(async () => (fetchPage.mock.calls.length === 1 ? page(row(1, { name: 'old' })) : next.promise))
    const table = engine({ fetchPage, sources: [{ intervalMs: 3000, fetch }] })
    await table.reload() // the run right after it is still on its way
    const reloading = table.reload()
    expect(fetch.mock.calls[0][1].aborted).toBe(true)
    early.resolve([[1, { name: 'stale' }]]) // answered anyway
    await vi.advanceTimersByTimeAsync(0)
    window.dispatchEvent(new Event('online')) // a catch-up during the load waits for it too
    await vi.advanceTimersByTimeAsync(10_000)
    expect(fetch).toHaveBeenCalledTimes(1) // nothing during the load
    expect(table.record(1)!.name).toBe('old')

    next.resolve(page(row(1, { name: 'new' })))
    await reloading
    expect(table.record(1)!.name).toBe('new')
    expect(fetch).toHaveBeenCalledTimes(2) // right after the load
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it('never overlaps a source with itself, even when its request ignores the abort', async () => {
    const stuck = deferred<LivePatches<Row>>()
    const fetch = vi.fn(() => (fetch.mock.calls.length === 1 ? stuck.promise : none()))
    const table = engine({ fetchPage: async () => page(row(1, { name: 'old' })), sources: [{ intervalMs: 3000, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000)
    await table.reload() // aborts the request, which keeps going
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetch).toHaveBeenCalledTimes(1)
    stuck.resolve([[1, { name: 'stale' }]])
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(2) // the beat that came due runs once the old request is done
    expect(table.record(1)!.name).toBe('old')
  })

  it('turns stale after a failed first load, retries it on the back-off beat and runs page tasks meanwhile', async () => {
    let fail = true
    const fetchPage = vi.fn(async () => {
      if (fail) throw new Error('down')
      return page(row(1))
    })
    const task = vi.fn(async () => undefined)
    const table = engine({ fetchPage, tasks: [{ intervalMs: 5000, run: task }] })
    await expect(table.reload()).rejects.toThrow('down')
    expect(table.status.value).toBe('stale')
    expect(table.loading.value).toBe(false)

    await vi.advanceTimersByTimeAsync(3000) // retry at +3 s fails; the next one waits 6 s
    expect(fetchPage).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(5000)
    expect(task).toHaveBeenCalledTimes(1) // with no rows yet
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchPage).toHaveBeenCalledTimes(3)
    fail = false
    await vi.advanceTimersByTimeAsync(12_000)
    expect(fetchPage).toHaveBeenCalledTimes(4)
    expect(table.ids.value).toEqual([1])
    expect(table.status.value).toBe('live')
  })

  it('keeps the rows when a reload fails and reloads at once when the network is back', async () => {
    let fail = false
    const fetchPage = vi.fn(async () => {
      if (fail) throw new Error('offline')
      return page(row(1))
    })
    const table = engine({ fetchPage })
    await table.reload()
    fail = true
    await expect(table.reload()).rejects.toThrow('offline')
    expect([table.ids.value, table.transitioning.value, table.status.value]).toEqual([[1], false, 'stale'])
    fail = false
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchPage).toHaveBeenCalledTimes(3)
    expect(table.status.value).toBe('live')
  })

  it('runs page-level tasks with no rows, and runs one named job now on refresh()', async () => {
    const stats = vi.fn(async () => undefined)
    const rows = vi.fn(none)
    const table = engine({
      fetchPage: async () => page(),
      sources: [{ name: 'rows', intervalMs: 3000, fetch: rows }],
      tasks: [{ name: 'stats', intervalMs: 10_000, run: stats }]
    })
    await table.reload()
    await vi.advanceTimersByTimeAsync(10_000)
    expect([stats.mock.calls.length, rows.mock.calls.length]).toEqual([2, 0]) // after the load, then on its beat

    await vi.advanceTimersByTimeAsync(4000)
    await table.refresh('stats')
    expect(stats).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(9999) // its beat starts over from the refresh
    expect(stats).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1)
    expect(stats).toHaveBeenCalledTimes(4)
  })

  it('runs partial sources and page tasks right after every successful load, the engine\'s retry included', async () => {
    let current = page(row(1))
    let fail = false
    const fetchPage = vi.fn(async () => {
      if (fail) throw new Error('down')
      return current
    })
    const partial = vi.fn(async (ids: LiveRowId[]): Promise<LivePatches<Row>> => ids.map((id) => [id, { today: 1 }] as const))
    const whole = vi.fn(none)
    const stats = vi.fn(async () => undefined)
    const table = engine({
      fetchPage,
      membershipIntervalMs: 30_000,
      sources: [{ intervalMs: 30_000, fetch: partial }, { intervalMs: 3000, complete: true, fetch: whole }],
      tasks: [{ intervalMs: 30_000, run: stats }]
    })
    const calls = () => [partial, stats, whole, fetchPage].map((f) => f.mock.calls.length)
    await table.reload()
    expect(calls()).toEqual([1, 1, 0, 1]) // whole rows and the membership check keep their beat

    current = page(row(2)) // another page
    await table.reload()
    await vi.advanceTimersByTimeAsync(0)
    expect(table.record(2)!.today).toBe(1)
    expect(calls()).toEqual([2, 2, 0, 2])

    fail = true
    await expect(table.reload()).rejects.toThrow('down')
    expect(calls()).toEqual([2, 2, 0, 3])
    fail = false
    await vi.advanceTimersByTimeAsync(2999)
    await vi.advanceTimersByTimeAsync(1) // the retry at 3 s
    expect(calls()).toEqual([3, 3, 0, 4])
  })

  it('remembers refresh() asked before the first load or during one, and runs it at once while paused', async () => {
    const whole = vi.fn(none) // a whole-row source: no run of its own after a load
    const next = deferred<LivePage<Row>>()
    const fetchPage = vi.fn(() => (fetchPage.mock.calls.length === 1 ? Promise.resolve(page(row(1))) : next.promise))
    const table = engine({ fetchPage, sources: [{ name: 'rows', intervalMs: 30_000, complete: true, fetch: whole }] })
    await table.refresh('rows')
    expect(whole).not.toHaveBeenCalled()
    await table.reload()
    expect(whole).toHaveBeenCalledTimes(1)

    const reloading = table.reload()
    await table.refresh('rows')
    table.pause() // before the load ends
    next.resolve(page(row(1)))
    await reloading
    expect(whole).toHaveBeenCalledTimes(2)

    await table.refresh('rows')
    expect(whole).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(whole).toHaveBeenCalledTimes(3) // no beat while paused
  })

  it('keeps a beat going when its aborted request outlives a load that ended while the tab was hidden', async () => {
    const stuck = deferred<LivePatches<Row>>()
    const fetch = vi.fn(() => (fetch.mock.calls.length === 1 ? stuck.promise : none()))
    const next = deferred<LivePage<Row>>()
    const fetchPage = vi.fn(() => (fetchPage.mock.calls.length === 1 ? Promise.resolve(page(row(1))) : next.promise))
    const table = engine({ fetchPage, sources: [{ intervalMs: 3000, complete: true, fetch }] })
    await table.reload()
    await vi.advanceTimersByTimeAsync(3000) // out, and its request ignores the abort
    const reloading = table.reload()
    setHidden(true)
    next.resolve(page(row(1)))
    await reloading // ends while hidden: nothing is scheduled
    setHidden(false) // the catch-up skips the beat while its run is out
    await vi.advanceTimersByTimeAsync(0)
    stuck.resolve([])
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('runs a load-only source and task after each load and on refresh only, and retries a failure', async () => {
    let fail = false
    const fetch = vi.fn(async (): Promise<LivePatches<Row>> => {
      if (fail) throw new Error('down')
      return []
    })
    const run = vi.fn(async () => undefined)
    const table = engine({
      fetchPage: async () => page(row(1)),
      sources: [{ name: 'quota', intervalMs: 0, fetch }],
      tasks: [{ name: 'trend', intervalMs: 0, run }]
    })
    const calls = () => [fetch.mock.calls.length, run.mock.calls.length]
    await table.reload()
    await vi.advanceTimersByTimeAsync(50)
    expect(calls()).toEqual([1, 1]) // once after the load, no loop
    await vi.advanceTimersByTimeAsync(120_000)
    setHidden(true)
    setHidden(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(calls()).toEqual([1, 1]) // no beat, no catch-up on visible

    await table.refresh('quota')
    await table.refresh('trend')
    expect(calls()).toEqual([2, 2])
    await table.reload()
    expect(calls()).toEqual([3, 3])

    fail = true
    await table.refresh('quota')
    expect(table.status.value).toBe('stale')
    fail = false
    await vi.advanceTimersByTimeAsync(3000) // retried on the back-off beat
    expect(fetch).toHaveBeenCalledTimes(5)
    expect(table.status.value).toBe('live')
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetch).toHaveBeenCalledTimes(5)
  })

  it('treats intervals that are not positive finite numbers as load-only and caps huge ones: no request loop', async () => {
    const fetch = vi.fn(none)
    const fetchPage = vi.fn(async () => page(row(1)))
    const table = engine({
      fetchPage,
      membershipIntervalMs: Infinity, // off
      sources: [Infinity, -1, Number.NaN, 2 ** 31].map((intervalMs) => ({ intervalMs, fetch }))
    })
    await table.reload()
    await vi.advanceTimersByTimeAsync(1000)
    expect([fetch.mock.calls.length, fetchPage.mock.calls.length]).toEqual([4, 1]) // once each after the load
  })

  it('holds only the membership check while membershipActive is false and runs it at once when it turns true', async () => {
    const active = ref(true)
    const fetchPage = vi.fn(async () => page(row(1)))
    const task = vi.fn(async () => {})
    const table = engine({ fetchPage, membershipIntervalMs: 5000, membershipActive: () => active.value, tasks: [{ intervalMs: 30_000, run: task }] })
    await table.reload()
    expect(fetchPage).toHaveBeenCalledTimes(1)

    active.value = false
    await vi.advanceTimersByTimeAsync(30_000)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(task).toHaveBeenCalledTimes(2) // after the load, then on its own 30 s beat
    expect(table.status.value).toBe('live')

    active.value = true
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchPage).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(5000)
    expect(fetchPage).toHaveBeenCalledTimes(3)
  })

  it('keeps a failed membership check failing while it is held back: the idle beats clear nothing (7j-5)', async () => {
    const active = ref(true)
    let down = false
    const fetchPage = vi.fn(async () => {
      if (down) throw new Error('down')
      return page(row(1))
    })
    const table = engine({ fetchPage, membershipIntervalMs: 5000, membershipActive: () => active.value })
    await table.reload()
    down = true
    await vi.advanceTimersByTimeAsync(5000)
    expect(table.status.value).toBe('stale')

    active.value = false
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchPage).toHaveBeenCalledTimes(2)
    expect(table.status.value).toBe('stale')

    down = false
    active.value = true
    await vi.advanceTimersByTimeAsync(0)
    expect(table.status.value).toBe('live')
  })

  it('pauses and resumes by hand, catching up on resume', async () => {
    const fetch = vi.fn(async () => [] as Array<readonly [LiveRowId, Partial<Row>]>)
    const table = engine({ fetchPage: async () => page(row(1)), sources: [{ intervalMs: 3000, complete: true, fetch }] })
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
