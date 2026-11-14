/**
 * One clock for the whole page: a "now" that ticks every second for countdowns, so cells keep only the end time the
 * backend sent and no cell runs its own timer.
 *
 *   const now = useLiveClock()            // Readonly<Ref<number>>, ms since epoch, server-corrected
 *   secondsLeft = Math.ceil((Date.parse(until) - now.value) / 1000)
 *
 * The clock runs while at least one scope uses it and the tab is visible; hidden tabs do not tick. The API client
 * passes every response's `Date` header to noteServerDate(), which corrects the local clock's offset from the server.
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

/** The shared, server-corrected "now" in ms. Stops ticking when the calling scope is disposed by the last user. */
export function useLiveClock(): Readonly<Ref<number>> {
  if (users++ === 0 && typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibilityChange)
  start()
  if (getCurrentScope()) {
    onScopeDispose(() => {
      if (--users > 0) return
      stop()
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibilityChange)
    })
  }
  return readonly(now)
}

/** Server time now, in ms: the local clock plus the measured offset. */
export const serverNow = () => Date.now() + offsetMs

/**
 * Feeds one response's `Date` header. The header is cut to the second, so the server time is taken as the middle of
 * that second; the offset only moves when it is off by more than a second.
 */
export function noteServerDate(header: unknown, receivedAt = Date.now()) {
  if (typeof header !== 'string' || header === '') return
  const server = Date.parse(header)
  if (!Number.isFinite(server)) return
  const estimate = server + MIN_CORRECTION_MS / 2 - receivedAt
  if (Math.abs(estimate - offsetMs) <= MIN_CORRECTION_MS) return
  offsetMs = estimate
  tick()
}
