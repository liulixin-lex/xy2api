/**
 * One clock for the whole page: a "now" that ticks every second for countdowns, so cells keep only the end time the
 * backend sent and no cell runs its own timer.
 *
 *   const now = useLiveClock()            // Readonly<Ref<number>>, ms since epoch, server-corrected
 *   secondsLeft = Math.ceil((Date.parse(until) - now.value) / 1000)
 *
 * The clock runs while at least one scope (component setup, effectScope) uses it and the tab is visible; hidden tabs do
 * not tick. A call outside any scope gets the same value but does not keep the clock running. The API client passes
 * every response's `Date` header and the request's send time to noteServerDate(), which corrects the offset from the
 * server.
 */
import { getCurrentScope, onScopeDispose, readonly, ref, type Ref } from 'vue'

const TICK_MS = 1000
/** The Date header has whole seconds; smaller differences are noise. */
const MIN_CORRECTION_MS = 1000

let offsetMs = 0
const now = ref(Date.now())
let users = 0
let timer: ReturnType<typeof setInterval> | null = null

const pageHidden = () => typeof document !== 'undefined' && document.hidden
const tick = () => {
  now.value = Date.now() + offsetMs
}

function start() {
  if (timer !== null || users === 0 || pageHidden()) return
  tick()
  timer = setInterval(tick, TICK_MS)
}

function stop() {
  if (timer === null) return
  clearInterval(timer)
  timer = null
}

function onVisibilityChange() {
  if (pageHidden()) stop()
  else start()
}

const shared = readonly(now)

/** The shared, server-corrected "now" in ms. Ticks while a scope uses it; stops when the last such scope is disposed. */
export function useLiveClock(): Readonly<Ref<number>> {
  if (!getCurrentScope()) return shared
  if (users++ === 0 && typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibilityChange)
  start()
  onScopeDispose(() => {
    if (--users > 0) return
    stop()
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibilityChange)
  })
  return shared
}

/**
 * Feeds one response's `Date` header. The server stamped it somewhere between sending and receiving, taken as the
 * midpoint of the two; the header is cut to the second, so the server time is taken as the middle of that second. The
 * offset only moves when it is off by more than a second.
 */
export function noteServerDate(header: unknown, sentAt?: number, receivedAt = Date.now()) {
  if (typeof header !== 'string' || header === '') return
  const server = Date.parse(header)
  if (!Number.isFinite(server)) return
  const local = sentAt === undefined ? receivedAt : (sentAt + receivedAt) / 2
  const estimate = server + MIN_CORRECTION_MS / 2 - local
  if (Math.abs(estimate - offsetMs) <= MIN_CORRECTION_MS) return
  offsetMs = estimate
  tick()
}
