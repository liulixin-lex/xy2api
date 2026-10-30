import { onUnmounted, ref } from 'vue'
import { isStepUpBlocked, isStepUpCancelled, StepUpCancelledError, useStepUp } from '@/composables/useStepUp'
import type { OpenAITotpTask } from '@/api/admin/openaiTotp'

export const normalizeTotpSecret = (secret: string) => secret.replace(/\s/g, '').toUpperCase()
export const validTotpCredentials = (email: string, password: string, secret: string) =>
  /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim()) && password.length > 0 && /^[A-Z2-7]{16,}={0,6}$/.test(normalizeTotpSecret(secret))

export function isOpenAITotpTaskActive(task: OpenAITotpTask | null | undefined) {
  return Boolean(task && ['queued', 'running', 'enrolling', 'prepared', 'activating', 'verifying'].includes(task.state))
}

// Never surface an upstream message, exception, or unknown error code: it may
// contain login secrets. Only these locally translated categories reach the UI.
export function openAITotpErrorKey(error: unknown): string {
  if (isStepUpBlocked(error)) return 'stepUpBlocked'
  const { code, reason, status } = (error ?? {}) as { code?: unknown; reason?: unknown; status?: number }
  const marker = [code, reason].filter((value): value is string => typeof value === 'string').join(' ').toUpperCase()
  if (marker.includes('WORKER') || marker.includes('RUNTIME') || marker.includes('JOURNAL')) return 'workerUnavailable'
  if (status === 409 || marker.includes('REVISION') || marker.includes('CONFLICT') || marker.includes('LOCKED')) return 'conflict'
  if (marker.includes('ENCRYPTION')) return 'encryptionUnavailable'
  if (status === 403) return 'forbidden'
  return 'requestFailed'
}

export function openAITotpTaskErrorKey(code?: string): string {
  return ['login_failed', 'mfa_rejected', 'verification_failed', 'runtime_unavailable', 'journal_unavailable', 'worker_timeout', 'identity_mismatch'].includes(code ?? '') ? code! : 'operation_failed'
}

/** A dialog owns one request scope, including step-up prompts and polling. */
export function useOpenAITotpDialog(active: () => boolean) {
  const busy = ref(false)
  const errorKey = ref('')
  const stepUp = useStepUp()
  let generation = 0
  let abortController = new AbortController()
  let pollTimer: ReturnType<typeof setTimeout> | undefined

  function stopPolling() {
    if (pollTimer !== undefined) clearTimeout(pollTimer)
    pollTimer = undefined
  }

  function reset() {
    generation++
    stopPolling()
    abortController.abort()
    abortController = new AbortController()
    stepUp.onCancel()
    busy.value = false
    errorKey.value = ''
  }

  function capture() {
    const version = generation
    const signal = abortController.signal
    return { signal, current: () => version === generation && !signal.aborted && active() }
  }

  async function run<T>(action: (signal: AbortSignal) => Promise<T>, sensitive = true): Promise<{ value: T; current: () => boolean } | undefined> {
    if (busy.value || !active()) return
    const scope = capture()
    busy.value = true
    errorKey.value = ''
    const guardedAction = () => {
      if (!scope.current()) throw new StepUpCancelledError()
      return action(scope.signal)
    }
    try {
      const value = await (sensitive ? stepUp.run(guardedAction) : guardedAction())
      if (scope.current()) return { value, current: scope.current }
    } catch (error) {
      if (scope.current() && !isStepUpCancelled(error)) errorKey.value = openAITotpErrorKey(error)
    } finally {
      if (scope.current()) busy.value = false
    }
  }

  function poll(read: () => Promise<void>) {
    stopPolling()
    const scope = capture()
    pollTimer = setTimeout(() => {
      pollTimer = undefined
      if (scope.current()) void read()
    }, 2000)
  }

  onUnmounted(reset)
  return { busy, errorKey, stepUp, reset, capture, run, poll, stopPolling }
}
