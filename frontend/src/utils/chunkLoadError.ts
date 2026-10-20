/**
 * 动态 chunk 加载失败的识别与恢复。
 * 典型场景：新版本部署后旧页面仍引用已删除的 chunk，路由跳转或打开按需加载的弹窗时 404。
 */

const RELOAD_KEY = 'chunk_reload_attempted'
const RELOAD_COOLDOWN_MS = 10_000

const CHUNK_ERROR_PATTERNS = [
  'Failed to fetch dynamically imported module', // Chromium
  'error loading dynamically imported module', // Firefox
  'Importing a module script failed', // Safari
  'Unable to preload CSS', // Vite CSS 预加载
  'Loading chunk',
  'Loading CSS chunk'
]

export function isChunkLoadError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const { name, message } = error as { name?: unknown; message?: unknown }
  if (name === 'ChunkLoadError') return true
  return typeof message === 'string' && CHUNK_ERROR_PATTERNS.some((pattern) => message.includes(pattern))
}

/**
 * 整页加载以拿到新的 index.html。给定目标地址时直接打开目标页（用户原本要去的页面），
 * 否则刷新当前页。10 秒内只尝试一次，避免资源确实缺失时循环刷新；返回是否已发起恢复。
 */
export function recoverFromChunkLoadError(targetUrl?: string): boolean {
  if (typeof window === 'undefined') return false
  try {
    const last = Number(sessionStorage.getItem(RELOAD_KEY) || 0)
    const now = Date.now()
    if (last && now - last < RELOAD_COOLDOWN_MS) return false
    sessionStorage.setItem(RELOAD_KEY, String(now))
  } catch {
    // sessionStorage 不可用时仍尝试一次
  }
  const current = window.location.pathname + window.location.search + window.location.hash
  if (targetUrl && targetUrl !== current) {
    window.location.assign(targetUrl)
  } else {
    window.location.reload()
  }
  return true
}
