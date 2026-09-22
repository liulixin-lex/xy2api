import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { usageExportAPI, type ExportScope, type UsageExportTask } from '@/api/usageExport'

export function useUsageExports(scope: ExportScope) {
  const { locale } = useI18n()
  const tasks = ref<UsageExportTask[]>([])
  const total = ref(0)
  const page = ref(1)
  const submitting = ref(false)
  const loading = ref(false)
  const pendingAction = ref('')
  const error = ref('')
  const waiting = ref(false)
  const active = computed(() => tasks.value.some(t => t.status === 'queued' || t.status === 'running'))
  const busy = computed(() => submitting.value || active.value)
  let disposed = false
  let polling: ReturnType<typeof setTimeout> | undefined
  let observer: AbortController | undefined
  const operations = new Set<AbortController>()
  let pending: { fingerprint: string; key: string; params: Record<string, unknown> } | undefined
  const sleep = (ms: number, signal: AbortSignal) => new Promise<void>((resolve, reject) => {
    if (signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
    const abort = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); reject(new DOMException('Aborted', 'AbortError')) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, ms)
    signal.addEventListener('abort', abort, { once: true })
  })
  async function retry<T>(operation: () => Promise<T>, signal: AbortSignal): Promise<T> {
    const start = Date.now()
    for (let attempt = 0; ; attempt++) {
      try { return await operation() } catch (e) {
        const issue = e as { status?: number; retryAfterMs?: number; code?: string }
        if (signal.aborted || issue.code === 'EXPORT_DISABLED' || ![0, 429, 502, 503, 504].includes(issue.status ?? -1)) throw e
        const delay = Math.max(0, issue.retryAfterMs ?? Math.min(30000, 2000 * 2 ** attempt)) + 100 + Math.random() * 400
        if (Date.now() - start + delay > 120000 || attempt >= 5) throw e
        waiting.value = true
        try { await sleep(delay, signal) } finally { waiting.value = false }
      }
    }
  }
  function report(e: unknown) {
    const code = (e as { code?: string }).code
    error.value = code?.startsWith('EXPORT_') ? code : 'EXPORT_CONNECTION_FAILED'
  }
  function schedule() {
    clearTimeout(polling)
    if (!disposed && !document.hidden && active.value && !error.value) polling = setTimeout(() => void refresh(), 5000)
  }
  async function refresh() {
    if (disposed || document.hidden || loading.value) return
    observer?.abort()
    const controller = new AbortController(); observer = controller
    loading.value = true; error.value = ''
    try {
      const result = await retry(() => usageExportAPI.list(scope, page.value, controller.signal), controller.signal)
      if (!controller.signal.aborted) { tasks.value = result.items; total.value = result.total }
    } catch (e) { if (!controller.signal.aborted) report(e) }
    finally { loading.value = false; schedule() }
  }
  async function create(params: Record<string, unknown>) {
    if (submitting.value || disposed) return
    if (active.value) { page.value = 1; await refresh(); return }
    observer?.abort()
    const frozen = JSON.parse(JSON.stringify(params)) as Record<string, unknown>
    delete frozen.page; delete frozen.page_size; delete frozen.exact_total
    frozen.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
    frozen.language = locale.value
    const fingerprint = JSON.stringify(frozen)
    if (!pending || pending.fingerprint !== fingerprint) pending = { fingerprint, key: crypto.randomUUID(), params: frozen }
    const request = pending
    const controller = new AbortController(); operations.add(controller)
    submitting.value = true; error.value = ''; clearTimeout(polling)
    try {
      const task = await retry(() => usageExportAPI.create(scope, request.params, request.key, controller.signal), controller.signal)
      pending = undefined
      if (!disposed) { page.value = 1; tasks.value = [task, ...tasks.value.filter(t => t.id !== task.id)]; total.value = Math.max(total.value, tasks.value.length) }
    } catch (e) { if (!controller.signal.aborted) report(e) }
    finally { operations.delete(controller); submitting.value = false; schedule() }
  }
  async function action(task: UsageExportTask, kind: 'cancel' | 'delete' | 'download-ticket') {
    if (pendingAction.value) return
    const controller = new AbortController(); operations.add(controller); pendingAction.value = task.id; error.value = ''
    try {
      const result = await retry(() => usageExportAPI.action(scope, task.id, kind, controller.signal), controller.signal)
      if (kind === 'download-ticket' && result?.url) {
        const target = new URL(result.url, window.location.origin)
        if (target.origin !== window.location.origin || !target.pathname.endsWith(`/${task.id}/download`)) throw new Error('Invalid download location')
        const link = document.createElement('a'); link.href = target.href; link.download = ''; link.click()
      } else await refresh()
    } catch (e) { if (!controller.signal.aborted) report(e) }
    finally { operations.delete(controller); pendingAction.value = '' }
  }
  function visibility() { if (document.hidden) { clearTimeout(polling); observer?.abort() } else void refresh() }
  watch(page, () => void refresh())
  onMounted(() => { document.addEventListener('visibilitychange', visibility); void refresh() })
  onUnmounted(() => { disposed = true; clearTimeout(polling); observer?.abort(); operations.forEach(c => c.abort()); document.removeEventListener('visibilitychange', visibility) })
  return { tasks, total, page, loading, busy, pendingAction, error, waiting, create, refresh, action }
}
