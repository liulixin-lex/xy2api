import { computed, readonly, ref } from 'vue'

/**
 * 动效偏好。两条通道任一开启即减少动画：
 * 1) 系统设置 prefers-reduced-motion；2) 用户在菜单里打开“减少动画”（本地保存）。
 * 结果写到 <html data-motion="reduced|full">，CSS（styles/motion.css）与 JS 动画都据此判断。
 */
export type MotionPreference = 'auto' | 'reduced'

const STORAGE_KEY = 'motion_preference'
const REDUCED_QUERY = '(prefers-reduced-motion: reduce)'

const preference = ref<MotionPreference>('auto')
const systemReduced = ref(false)
const reducedMotion = computed(() => preference.value === 'reduced' || systemReduced.value)
let initialized = false

function readStoredPreference(): MotionPreference {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'reduced' ? 'reduced' : 'auto'
  } catch {
    return 'auto'
  }
}

function applyToDocument() {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.motion = reducedMotion.value ? 'reduced' : 'full'
}

/** 在应用挂载前调用一次，避免首屏先播放动画再关闭。 */
export function initMotionPreference() {
  if (initialized || typeof window === 'undefined') return
  initialized = true
  preference.value = readStoredPreference()
  const query = typeof window.matchMedia === 'function' ? window.matchMedia(REDUCED_QUERY) : null
  systemReduced.value = !!query?.matches
  const onChange = (event: MediaQueryListEvent) => {
    systemReduced.value = event.matches
    applyToDocument()
  }
  if (query && typeof query.addEventListener === 'function') query.addEventListener('change', onChange)
  else query?.addListener?.(onChange)
  applyToDocument()
}

export function setMotionPreference(value: MotionPreference) {
  preference.value = value
  try {
    if (value === 'reduced') localStorage.setItem(STORAGE_KEY, 'reduced')
    else localStorage.removeItem(STORAGE_KEY)
  } catch {
    // 本地存储不可用时只在当前会话生效
  }
  applyToDocument()
}

/** 非响应式读取，供一次性的 JS 动画（WAAPI、数字滚动）在启动前判断。 */
export function isReducedMotion(): boolean {
  if (typeof document !== 'undefined' && document.documentElement.dataset.motion) {
    return document.documentElement.dataset.motion === 'reduced'
  }
  return reducedMotion.value
}

export function useMotionPreference() {
  initMotionPreference()
  return {
    preference: readonly(preference),
    systemReduced: readonly(systemReduced),
    reducedMotion,
    setPreference: setMotionPreference,
    toggleReduced: () => setMotionPreference(preference.value === 'reduced' ? 'auto' : 'reduced')
  }
}
