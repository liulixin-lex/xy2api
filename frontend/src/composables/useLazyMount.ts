import { defineAsyncComponent, ref, watch, type AsyncComponentLoader, type Ref, type WatchSource } from 'vue'
import { isChunkLoadError, recoverFromChunkLoadError } from '@/utils/chunkLoadError'

/**
 * 首次变为 true 后一直保持 true。
 * 用于大弹窗按需挂载：页面打开时不执行弹窗的 setup，首次打开才挂载；
 * 之后保留实例，关闭时照常播放离场动画，再次打开与原先行为一致。
 */
export function useLazyMount(source: WatchSource<unknown>): Readonly<Ref<boolean>> {
  const mounted = ref(false)
  watch(
    source,
    (value) => {
      if (value) mounted.value = true
    },
    { immediate: true, flush: 'sync' }
  )
  return mounted
}

/** 空闲时预取异步组件的 chunk，首次打开时无需等待网络。 */
export function prefetchWhenIdle(loaders: Array<() => Promise<unknown>>, timeout = 2000): () => void {
  if (typeof window === 'undefined') return () => {}
  let cancelled = false
  const run = () => {
    if (cancelled) return
    for (const load of loaders) load().catch(() => {})
  }
  if (typeof window.requestIdleCallback === 'function') {
    const id = window.requestIdleCallback(run, { timeout })
    return () => {
      cancelled = true
      window.cancelIdleCallback(id)
    }
  }
  const id = window.setTimeout(run, timeout)
  return () => {
    cancelled = true
    window.clearTimeout(id)
  }
}

/**
 * 按需加载的组件。chunk 加载失败（新版本部署后旧文件已删除）时整页刷新拿到新版本，
 * 而不是让弹窗静默打不开。
 */
export function defineLazyComponent(loader: AsyncComponentLoader) {
  return defineAsyncComponent({
    loader,
    onError(error, _retry, fail) {
      if (isChunkLoadError(error)) recoverFromChunkLoadError()
      fail()
    }
  })
}
